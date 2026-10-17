"""Meeting-derived field scope; no AWS client, cached authorization or writes."""
from datetime import datetime, timezone
from decimal import Decimal
import hashlib
import os
import re

CUTOFF_ENV = 'SUMMARY_PRIVACY_LEGACY_MEMBER_CUTOFF'
READER_ENV = 'SUMMARY_PRIVACY_READER_ENABLED'
RESTRICTION = 'SHARING_REGEN_REQUIRED'
DERIVED_FIELDS = ('content', 'actionItems', 'insights', 'sentiment', 'liveSummary',
                  'tags', 'linkedMeetingIds')
RFC3339 = re.compile(r'(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.(\d{1,9}))?Z')


def _utc(value):
    if type(value) is not str:
        return None
    match = RFC3339.fullmatch(value)
    if not match:
        return None
    try:
        second = datetime.strptime(match[1], '%Y-%m-%dT%H:%M:%S').replace(tzinfo=timezone.utc)
        # Go AddedAt and the shared cutoff accept RFC3339 nanoseconds.
        # Truncating either timestamp to microseconds can grandfather a new
        # membership on the wrong side of the activation boundary.
        return second, int((match[2] or '').ljust(9, '0'))
    except ValueError:
        return None


def _number(value):
    return type(value) in (int, Decimal) and not isinstance(value, bool)


def needs_summary_guard(member, cutoff=None):
    if type(member) is not dict:
        return True
    version = member.get('summaryPrivacyVersion', 0)
    if not _number(version) or version != 0:
        return True
    before = _utc(os.environ.get(CUTOFF_ENV, '') if cutoff is None else cutoff)
    added = _utc(member.get('addedAt'))
    return before is None or added is None or added >= before


def enforce_summary_guard(member):
    if type(member) is not dict:
        return True
    version = member.get('summaryPrivacyVersion', 0)
    if not _number(version) or version != 0:
        return True
    enabled = os.environ.get(READER_ENV, '')
    if enabled in ('', '0'):
        # Installation stage only, not proof that this is a legacy grant.
        return False
    return needs_summary_guard(member) if enabled == '1' else True


def safe_summary(record):
    if type(record) is not dict:
        return False
    privacy = record.get('summaryPrivacy')
    if (type(privacy) is not dict or not _number(privacy.get('version'))
            or privacy.get('version') != 1 or privacy.get('state') != 'safe'
            or type(privacy.get('generationId')) is not str or not privacy['generationId']
            or type(privacy.get('contentSha256')) is not str
            or re.fullmatch(r'[0-9a-f]{64}', privacy['contentSha256']) is None):
        return False
    # Metadata gates rely on the trusted publisher marker. Content writers
    # invalidate it atomically; full reads also verify the exact current bytes.
    if 'content' in record:
        content = record['content']
        return (type(content) is str
                and hashlib.sha256(content.encode()).hexdigest() == privacy['contentSha256'])
    return True


def project_meeting(record, restricted):
    projected = dict(record)
    if restricted:
        for field in DERIVED_FIELDS:
            projected.pop(field, None)
        projected['summaryReadRestriction'] = RESTRICTION
    return projected
