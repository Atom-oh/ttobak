"""Read a pinned legacy or immutable-version spill object for an authorized meeting."""
import re


def validate_transcript_ref(value, *, bucket_name, meeting_id, field):
    # Mirrors Go's repository.validateTranscriptRef. Never normalize or decode a key.
    if (
        not isinstance(value, str)
        or not isinstance(bucket_name, str)
        or not bucket_name
        or not isinstance(meeting_id, str)
        or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_-]{0,127}', meeting_id)
        or field not in ('transcriptA', 'transcriptB', 'transcriptSegments')
    ):
        raise ValueError('Invalid transcript storage reference')
    prefix = f's3://{bucket_name}/'
    if not value.startswith(prefix):
        raise ValueError('Invalid transcript storage reference')
    key = value[len(prefix):]
    base = re.escape(f'transcripts/{meeting_id}/{field}')
    if not re.fullmatch(base + r'(?:\.[0-9a-f]{32})?\.txt', key):
        raise ValueError('Invalid transcript storage reference')
    return key


def resolve_transcript(value, *, bucket_name, meeting_id, field, s3_client):
    if not isinstance(value, str) or not value.startswith('s3://'):
        return value
    key = validate_transcript_ref(
        value, bucket_name=bucket_name, meeting_id=meeting_id, field=field,
    )
    response = s3_client.get_object(Bucket=bucket_name, Key=key)
    with response['Body'] as body:
        return body.read().decode('utf-8')
