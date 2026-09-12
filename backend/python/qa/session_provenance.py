"""Source dependencies live beside session messages, never in Converse blocks."""
from decimal import Decimal

from source_revision import HEX_REVISION, IDENTIFIER, resource_identity
from manual_kb import shared_source_key


def new_source_state():
    return {'dependencies': [], 'replayable': True}


def valid_dependency(dependency):
    if not isinstance(dependency, dict):
        return False
    try:
        revision = dependency.get('sourceRevision')
        if not isinstance(revision, str) or HEX_REVISION.fullmatch(revision) is None:
            return False
        if 'legacyURI' in dependency:
            return (set(dependency) == {'legacyURI', 'sourceRevision'} and
                    isinstance(dependency['legacyURI'], str) and len(dependency['legacyURI']) <= 2048)
        if 'manualKey' in dependency:
            return (set(dependency) == {'manualKey', 'sourceRevision'}
                    and isinstance(dependency['manualKey'], str) and len(dependency['manualKey'].encode()) <= 1024)
        if 'sharedKey' in dependency:
            return set(dependency) == {'sharedKey', 'sourceRevision'} and shared_source_key(dependency['sharedKey'])
        resource_identity(dependency.get('sourcePK'), dependency.get('sourceSK'))
        attachment_id = dependency.get('attachmentId')
        if attachment_id is not None and (not dependency['sourceSK'].startswith('MEETING#')
                or not isinstance(attachment_id, str)
                or attachment_id != '*' and not IDENTIFIER.fullmatch(attachment_id)):
            return False
        return True
    except ValueError:
        return False


def _dependency_key(dependency):
    if 'legacyURI' in dependency:
        return ('legacy', dependency['legacyURI'])
    if 'manualKey' in dependency:
        return ('manual', dependency['manualKey'])
    if 'sharedKey' in dependency:
        return ('shared', dependency['sharedKey'])
    return ('canonical', dependency['sourcePK'], dependency['sourceSK'], dependency.get('attachmentId'))


def remember_source(state, dependency):
    if not valid_dependency(dependency):
        raise ValueError('Invalid source dependency')
    key = _dependency_key(dependency)
    for saved in state['dependencies']:
        if _dependency_key(saved) == key:
            if saved != dependency:
                raise RuntimeError('Source changed during this answer; retry with current content.')
            return
    state['dependencies'].append(dict(dependency))


def restore_sources(item, state, is_current):
    """Legacy/untracked histories cannot prove absence of private derived text."""
    dependencies = item.get('sourceDependencies')
    # DynamoDB resource reads deserialize all Number attributes as Decimal.
    version = item.get('sourceProvenanceVersion')
    valid_version = (type(version) is int or isinstance(version, Decimal) and version.is_finite())
    if (not valid_version or version != 1
            or item.get('sourceReplayable') is not True
            or not isinstance(dependencies, list)
            or not all(valid_dependency(dep) for dep in dependencies)):
        return False
    if not all(is_current(dependency) for dependency in dependencies):
        return False
    for dependency in dependencies:
        remember_source(state, dependency)
    return True


def validate_sources(state, is_current):
    if not all(is_current(dependency) for dependency in state['dependencies']):
        raise RuntimeError('Source access or content changed; retry with current content.')


def collect_detail(details, detail):
    if detail and detail not in details:
        details.append(dict(detail))
