"""FastAPI WebSocket server for real-time STT, translation, and question detection."""

import asyncio
import io
import json
import os
import signal
import time
import uuid
import wave
from typing import Any

import boto3
from fastapi import FastAPI, WebSocket, WebSocketDisconnect

# Shutdown event for graceful Spot interruption handling
shutdown_event = asyncio.Event()

# Track active connections for flushing on shutdown
active_connections: set["ConnectionState"] = set()

from detector import QuestionDetector
from stt import WhisperSTT
from translator import Translator

# Initialize FastAPI app
app = FastAPI(title="Ttobak Realtime STT Server")


def handle_sigterm(signum, frame):
    """Handle SIGTERM signal (Spot interruption warning)."""
    print("SIGTERM received - Spot interruption warning. Initiating graceful shutdown...")
    # Set the shutdown event - asyncio.Event is thread-safe for set()
    shutdown_event.set()


# Register SIGTERM handler for Spot interruption (2-minute warning)
signal.signal(signal.SIGTERM, handle_sigterm)

# S3 client for audio aggregation upload
s3_client = boto3.client("s3")
AUDIO_BUCKET = os.environ.get("AUDIO_BUCKET", "ttobak-data-180294183052-ap-northeast-2")

# Global singletons - loaded once at startup
stt_engine: WhisperSTT | None = None
translator_service: Translator | None = None
question_detector: QuestionDetector | None = None


@app.on_event("startup")
async def startup_event():
    """Initialize ML models and services on startup."""
    global stt_engine, translator_service, question_detector
    print("Loading Whisper model...")
    stt_engine = WhisperSTT()
    print("Whisper model loaded.")
    translator_service = Translator()
    question_detector = QuestionDetector()
    print("All services initialized.")


@app.on_event("shutdown")
async def shutdown_handler():
    """Flush all active connections on shutdown (Spot interruption or normal shutdown)."""
    print(f"Shutdown event triggered. Flushing {len(active_connections)} active connections...")
    shutdown_event.set()

    # Give connections time to detect shutdown and flush their audio
    await asyncio.sleep(2)

    # Force flush any remaining connections
    for state in list(active_connections):
        try:
            if state.aggregated_audio and len(state.aggregated_audio) >= 16000:
                await save_aggregated_audio(state)
                print(f"Force-flushed audio for session {state.session_id}")
        except Exception as e:
            print(f"Error flushing connection {state.session_id}: {e}")

    print("Shutdown flush complete.")


@app.get("/health")
async def health_check() -> dict[str, str]:
    """Health check endpoint for ALB target group."""
    return {"status": "ok", "model": "large-v3"}


class ConnectionState:
    """Per-connection state for WebSocket processing."""

    def __init__(self):
        self.audio_buffer = bytearray()
        self.sentence_buffer: list[str] = []
        self.word_count = 0
        self.last_process_time = time.time()
        self.source_lang = "ko"
        self.target_lang = "en"
        self.lock = asyncio.Lock()
        self.running = True

        # Audio aggregation for S3 upload on disconnect
        self.session_id = str(uuid.uuid4())
        self.user_id: str | None = None
        self.meeting_id: str | None = None
        self.aggregated_audio = bytearray()


async def process_audio(
    websocket: WebSocket,
    state: ConnectionState,
) -> None:
    """Background task to process accumulated audio."""
    # Audio chunk size: 0.5 seconds at 16kHz 16-bit mono = 16000 bytes
    # But we process every 8000 bytes (0.25 seconds) for lower latency
    CHUNK_SIZE = 8000
    PROCESS_INTERVAL = 0.5  # seconds

    while state.running:
        # Check for Spot interruption shutdown signal
        if shutdown_event.is_set():
            print(f"Shutdown detected in process_audio for session {state.session_id}")
            state.running = False
            break

        await asyncio.sleep(0.1)  # Check interval

        audio_to_process = b""
        async with state.lock:
            current_time = time.time()
            elapsed = current_time - state.last_process_time

            # Process if we have enough audio and enough time has passed
            if len(state.audio_buffer) >= CHUNK_SIZE and elapsed >= PROCESS_INTERVAL:
                # Copy buffer for processing
                audio_to_process = bytes(state.audio_buffer)
                state.audio_buffer.clear()
                state.last_process_time = current_time

        # Process outside the lock to avoid blocking
        if len(audio_to_process) >= CHUNK_SIZE:
            await process_audio_chunk(websocket, state, audio_to_process)


async def _translate_and_send(
    websocket: WebSocket,
    state: ConnectionState,
    text: str,
) -> None:
    """Translate text and send result via WebSocket (background task)."""
    try:
        loop = asyncio.get_event_loop()
        translated = await loop.run_in_executor(
            None,
            lambda: translator_service.translate(
                text, state.source_lang, state.target_lang
            ),
        )
        if translated:
            await websocket.send_json(
                {
                    "type": "translation",
                    "original": text,
                    "translated": translated,
                    "targetLang": state.target_lang,
                    "isFinal": True,
                }
            )
    except Exception as e:
        print(f"Translation error: {e}")


async def process_audio_chunk(
    websocket: WebSocket,
    state: ConnectionState,
    audio_data: bytes,
) -> None:
    """Process a chunk of audio through the STT pipeline."""
    try:
        # Run STT (CPU-bound, run in executor)
        loop = asyncio.get_event_loop()
        result = await loop.run_in_executor(
            None,
            lambda: stt_engine.transcribe(audio_data, state.source_lang),
        )

        text = result.get("text", "").strip()
        if not text:
            return

        # Send transcript
        await websocket.send_json(
            {
                "type": "transcript",
                "text": text,
                "isFinal": True,
            }
        )

        # Add to sentence buffer
        async with state.lock:
            state.sentence_buffer.append(text)
            # Keep only last 10 sentences
            if len(state.sentence_buffer) > 10:
                state.sentence_buffer = state.sentence_buffer[-10:]

        # Translate in background (non-blocking)
        asyncio.create_task(
            _translate_and_send(websocket, state, text)
        )

        # Count words (Korean: roughly 1 word per 2 characters)
        sentences_to_check: list[str] = []
        should_detect = False
        async with state.lock:
            if state.source_lang == "ko":
                state.word_count += len(text.replace(" ", "")) // 2
            else:
                state.word_count += len(text.split())

            # Run question detection every ~10 words
            if state.word_count >= 10:
                state.word_count = 0
                sentences_to_check = state.sentence_buffer[-3:]
                should_detect = True

        # Question detection (outside lock)
        if should_detect and sentences_to_check:
            questions = await loop.run_in_executor(
                None,
                lambda: question_detector.detect(sentences_to_check),
            )

            if questions:
                await websocket.send_json(
                    {
                        "type": "question",
                        "questions": questions,
                    }
                )

    except Exception as e:
        print(f"Audio processing error: {e}")
        # Don't crash, continue processing


async def handle_control_message(
    message: str,
    state: ConnectionState,
) -> bool:
    """Handle JSON control messages.

    Returns:
        False if connection should be closed, True otherwise.
    """
    try:
        data = json.loads(message)
        action = data.get("action")

        if action == "config":
            async with state.lock:
                state.source_lang = data.get("language", state.source_lang)
                state.target_lang = data.get("targetLang", state.target_lang)
                if data.get("userId"):
                    state.user_id = data["userId"]
                if data.get("meetingId"):
                    state.meeting_id = data["meetingId"]
            return True

        elif action == "stop":
            state.running = False
            return False

    except json.JSONDecodeError:
        print(f"Invalid JSON message: {message}")

    return True


async def save_aggregated_audio(state: ConnectionState) -> str | None:
    """Save aggregated audio chunks to S3 as WAV. Returns S3 key or None."""
    if not state.aggregated_audio or len(state.aggregated_audio) < 16000:  # < 0.5s
        return None

    try:
        # Create WAV file in memory (16kHz, 16-bit mono PCM)
        wav_buffer = io.BytesIO()
        with wave.open(wav_buffer, "wb") as wav_file:
            wav_file.setnchannels(1)
            wav_file.setsampwidth(2)
            wav_file.setframerate(16000)
            wav_file.writeframes(bytes(state.aggregated_audio))

        wav_buffer.seek(0)
        wav_data = wav_buffer.read()

        user_id = state.user_id or "anonymous"
        meeting_id = state.meeting_id or state.session_id
        s3_key = f"audio/{user_id}/{meeting_id}/realtime_{state.session_id}.wav"

        loop = asyncio.get_event_loop()
        await loop.run_in_executor(
            None,
            lambda: s3_client.put_object(
                Bucket=AUDIO_BUCKET,
                Key=s3_key,
                Body=wav_data,
                ContentType="audio/wav",
                Metadata={
                    "session-id": state.session_id,
                    "duration-seconds": str(len(state.aggregated_audio) // 32000),
                    "sample-rate": "16000",
                },
            ),
        )

        duration = len(state.aggregated_audio) // 32000
        print(f"Saved aggregated audio: {s3_key} ({len(wav_data)} bytes, ~{duration}s)")
        return s3_key

    except Exception as e:
        print(f"Failed to save aggregated audio: {e}")
        return None


@app.websocket("/ws")
async def websocket_endpoint(websocket: WebSocket) -> None:
    """Main WebSocket endpoint for real-time processing."""
    await websocket.accept()

    state = ConnectionState()
    process_task: asyncio.Task | None = None

    # Track this connection for shutdown flushing
    active_connections.add(state)

    try:
        # Start background audio processing task
        process_task = asyncio.create_task(process_audio(websocket, state))

        # Main receive loop
        while state.running:
            # Check for Spot interruption shutdown signal
            if shutdown_event.is_set():
                print(f"Spot interruption detected for session {state.session_id}. Flushing audio...")
                # Save audio before closing
                audio_key = await save_aggregated_audio(state)
                # Notify client about Spot interruption so it can fall back to Web Speech API
                try:
                    await websocket.send_json({
                        "type": "spot_interruption",
                        "message": "Server is shutting down due to Spot instance reclaim. Please reconnect or use fallback.",
                        "audioKey": audio_key,
                    })
                except Exception:
                    pass
                break

            try:
                # Use wait_for with timeout to allow checking shutdown_event periodically
                message = await asyncio.wait_for(websocket.receive(), timeout=1.0)

                if message["type"] == "websocket.disconnect":
                    break

                elif message["type"] == "websocket.receive":
                    if "bytes" in message:
                        # Binary frame = audio data
                        audio_bytes = message["bytes"]
                        async with state.lock:
                            state.audio_buffer.extend(audio_bytes)
                            # Aggregate for final S3 upload
                            state.aggregated_audio.extend(audio_bytes)

                    elif "text" in message:
                        # Text frame = JSON control message
                        should_continue = await handle_control_message(
                            message["text"], state
                        )
                        if not should_continue:
                            break

            except asyncio.TimeoutError:
                # Timeout is expected - allows us to check shutdown_event
                continue

            except WebSocketDisconnect:
                break

    except Exception as e:
        print(f"WebSocket error: {e}")

    finally:
        # Remove from active connections tracking
        active_connections.discard(state)

        # Cleanup
        state.running = False
        if process_task:
            process_task.cancel()
            try:
                await process_task
            except asyncio.CancelledError:
                pass

        # Save aggregated audio to S3 (skip if already saved during shutdown)
        audio_key = await save_aggregated_audio(state)
        if audio_key:
            try:
                await websocket.send_json({
                    "type": "audio_saved",
                    "key": audio_key,
                })
            except Exception:
                pass  # Connection may already be closed

        try:
            await websocket.close()
        except Exception:
            pass


if __name__ == "__main__":
    import uvicorn

    uvicorn.run(app, host="0.0.0.0", port=8000)
