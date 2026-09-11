"""Read only the exact repository-owned spill object for an authorized meeting."""
import re


def validate_transcript_ref(value, *, bucket_name, meeting_id, field):
    # Matches Go's repository.validateTranscriptRef key binding, plus an
    # explicit meeting ID charset check. Never normalize a supplied key.
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
