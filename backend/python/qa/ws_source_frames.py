"""Split completion attribution into bounded frames without discarding sources."""
import hashlib
import json

MAX_SOURCE_BYTES = 512 * 1024
MAX_SOURCE_FRAMES = 64


def wire_bytes(payload):
    return json.dumps(payload, ensure_ascii=False).encode('utf-8')


def completion_frames(payload, frame_bytes=30_000):
    if len(wire_bytes(payload)) <= frame_bytes:
        return [payload]
    sources, details = payload.get('sources', []), payload.get('sourceDetails', [])
    if type(sources) is not list or type(details) is not list:
        raise ValueError('Invalid completion attribution')
    attribution = wire_bytes({'sources': sources, 'sourceDetails': details})
    if len(attribution) > MAX_SOURCE_BYTES:
        raise ValueError('Completion attribution limit exceeded')
    batch = hashlib.sha256(attribution).hexdigest()
    final = {key: value for key, value in payload.items() if key not in ('sources', 'sourceDetails')}
    final.update(sourceBatchId=batch, sourceBatchCount=MAX_SOURCE_FRAMES)
    if len(wire_bytes(final)) > frame_bytes:
        raise ValueError('Answer exceeds completion frame limit')
    frames = []

    def empty():
        return {'type': 'answer_sources', 'sessionId': payload.get('sessionId'),
                'sourceBatchId': batch, 'sourceBatchIndex': len(frames),
                'sources': [], 'sourceDetails': []}

    current = empty()
    for field, values in (('sources', sources), ('sourceDetails', details)):
        for value in values:
            current[field].append(value)
            if len(wire_bytes(current)) <= frame_bytes:
                continue
            current[field].pop()
            if not current['sources'] and not current['sourceDetails']:
                raise ValueError('Single source exceeds frame limit')
            frames.append(current)
            if len(frames) >= MAX_SOURCE_FRAMES:
                raise ValueError('Source frame count exceeded')
            current = empty()
            current[field].append(value)
            if len(wire_bytes(current)) > frame_bytes:
                raise ValueError('Single source exceeds frame limit')
    if current['sources'] or current['sourceDetails']:
        frames.append(current)
    if not frames or len(frames) > MAX_SOURCE_FRAMES:
        raise ValueError('Source frame count exceeded')
    final['sourceBatchCount'] = len(frames)
    return [*frames, final]
