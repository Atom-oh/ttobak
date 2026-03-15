"""FastAPI WebSocket server for real-time STT, translation, and question detection."""

import asyncio
import json
import time
from typing import Any

from fastapi import FastAPI, WebSocket, WebSocketDisconnect

from detector import QuestionDetector
from stt import WhisperSTT
from translator import Translator

# Initialize FastAPI app
app = FastAPI(title="Ttobak Realtime STT Server")

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

        # Translate and send
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
                }
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
            return True

        elif action == "stop":
            state.running = False
            return False

    except json.JSONDecodeError:
        print(f"Invalid JSON message: {message}")

    return True


@app.websocket("/ws")
async def websocket_endpoint(websocket: WebSocket) -> None:
    """Main WebSocket endpoint for real-time processing."""
    await websocket.accept()

    state = ConnectionState()
    process_task: asyncio.Task | None = None

    try:
        # Start background audio processing task
        process_task = asyncio.create_task(process_audio(websocket, state))

        # Main receive loop
        while state.running:
            try:
                message = await websocket.receive()

                if message["type"] == "websocket.disconnect":
                    break

                elif message["type"] == "websocket.receive":
                    if "bytes" in message:
                        # Binary frame = audio data
                        async with state.lock:
                            state.audio_buffer.extend(message["bytes"])

                    elif "text" in message:
                        # Text frame = JSON control message
                        should_continue = await handle_control_message(
                            message["text"], state
                        )
                        if not should_continue:
                            break

            except WebSocketDisconnect:
                break

    except Exception as e:
        print(f"WebSocket error: {e}")

    finally:
        # Cleanup
        state.running = False
        if process_task:
            process_task.cancel()
            try:
                await process_task
            except asyncio.CancelledError:
                pass

        try:
            await websocket.close()
        except Exception:
            pass


if __name__ == "__main__":
    import uvicorn

    uvicorn.run(app, host="0.0.0.0", port=8000)
