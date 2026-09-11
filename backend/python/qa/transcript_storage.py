"""Read only the exact repository-owned spill object for an authorized meeting."""
import re


def resolve_transcript(value, *, bucket_name, meeting_id, field, s3_client):
    if not isinstance(value, str) or not value.startswith('s3://'):
        return value
    # Mirrors repository.validateTranscriptRef in Go. Do not URL-decode or
    # normalize a supplied key: the writer emits exactly this one spelling.
    if (
        not bucket_name
        or not isinstance(meeting_id, str)
        or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_-]{0,127}', meeting_id)
        or field not in ('transcriptA', 'transcriptB')
    ):
        raise ValueError('Invalid transcript storage reference')
    key = f'transcripts/{meeting_id}/{field}.txt'
    if value != f's3://{bucket_name}/{key}':
        raise ValueError('Invalid transcript storage reference')
    response = s3_client.get_object(Bucket=bucket_name, Key=key)
    with response['Body'] as body:
        return body.read().decode('utf-8')
