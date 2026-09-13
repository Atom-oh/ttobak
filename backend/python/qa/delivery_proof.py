"""Current-answer proof is independent of the smaller conversation replay budget."""
import copy
from decimal import Decimal

from session_provenance import (
    valid_dependency, _dependency_key, SourceValidationError, SourceUnavailable,
)
from tool_history import fingerprint, is_tool_dependency, valid_tool_dependency

MAX_DEPENDENCIES = 512
FINGERPRINT_LIMITS = {'byte_limit': 4 * 1024 * 1024, 'node_limit': 65536}


def valid_delivery_dependency(dependency):
    if is_tool_dependency(dependency):
        return valid_tool_dependency(dependency, **FINGERPRINT_LIMITS)
    return valid_dependency(dependency)


def _key(dependency):
    if 'readOnlyTool' in dependency:
        return ('readonly-tool', dependency['userId'], dependency['readOnlyTool'],
                fingerprint(dependency['toolInput'], **FINGERPRINT_LIMITS))
    return _dependency_key(dependency)


class DeliveryProof:
    def __init__(self):
        self.dependencies = {}
        self.initialized = False
        self.invalid = False
        self.overflow = False
        self.changed = False
        self.capacity_limited = False

    def reject(self):
        self.invalid = True

    def history_limited(self):
        self.capacity_limited = True

    def source(self, dependency):
        try:
            if not valid_delivery_dependency(dependency):
                self.reject()
                return
            key = _key(dependency)
            previous = self.dependencies.get(key)
            if previous is not None and previous != dependency:
                self.changed = True  # One answer cannot attest two revisions of a source.
            elif previous is None:
                if len(self.dependencies) >= MAX_DEPENDENCIES:
                    self.overflow = True
                else:
                    self.dependencies[key] = copy.deepcopy(dependency)
        except Exception:
            self.reject()

    def seed(self, state):
        # Called after successful history restoration, never for a discarded
        # candidate history. Initial source failures are not capacity overflow.
        self.initialized = True
        if state.get('replayable') is not True and not self.capacity_limited:
            self.reject()
        for dependency in state.get('dependencies', []):
            self.source(dependency)

    def read_sources(self, current):
        complete = current.get('replayable') is True and bool(current.get('dependencies'))
        if not complete:
            self.reject()
        for dependency in current.get('dependencies', []):
            self.source(dependency)
        return complete

    def readonly(self, user_id, name, arguments, value):
        # The strict callback already returned CompleteRead. Only a digest and
        # bounded inputs are retained; no large tool result is stored here.
        try:
            self.source({
                'readOnlyTool': name, 'toolInput': arguments, 'userId': user_id,
                'sourceRevision': fingerprint(['readonly-tool-v1', user_id, name, arguments, value],
                                              **FINGERPRINT_LIMITS),
            })
        except Exception:
            self.overflow = True

    def finish(self, state):
        if not self.initialized:
            self.seed(state)
        for dependency in state.get('dependencies', []):
            self.source(dependency)
        if state.get('replayable') is not True and not self.capacity_limited:
            self.reject()
        if self.changed:
            raise SourceValidationError()
        if self.invalid or self.overflow:
            raise SourceUnavailable()
        return {'deliveryVersion': 1, 'dependencies': list(self.dependencies.values()),
                'requestMeetingId': state.get('requestMeetingId'), 'replayable': True}


def validate_delivery(proof, is_current, history):
    dependencies = proof.get('dependencies')
    version = proof.get('deliveryVersion')
    if ((type(version) is not int and not (type(version) is Decimal and version.is_finite()))
            or version != 1 or type(dependencies) is not list
            or len(dependencies) > MAX_DEPENDENCIES or not all(valid_delivery_dependency(d) for d in dependencies)):
        raise SourceValidationError()
    try:
        for dependency in dependencies:
            current = (history.is_current(dependency, strict=True, **FINGERPRINT_LIMITS) if is_tool_dependency(dependency)
                       else is_current(dependency))
            if not current:
                raise SourceValidationError()
    except SourceValidationError:
        raise
    except Exception:
        raise SourceUnavailable() from None
