"""Bounded, current-user replay checks for an explicit readonly tool allowlist."""
from dataclasses import dataclass
from decimal import Decimal
import hashlib

from source_revision import HEX_REVISION, IDENTIFIER

READONLY_TOOLS = frozenset(('list_meetings', 'list_accounts', 'get_account_insights', 'get_account_brief'))
MAX_TOOL_DEPENDENCIES = 16
MAX_RESULT_BYTES = 65536
MAX_NODES = 4096
MAX_DEPTH = 12
INSIGHT_TYPES = frozenset(('trend', 'need', 'competitive', 'risk', 'opportunity', 'tech', 'stakeholder', 'action'))


@dataclass(frozen=True)
class CompleteRead:
    """Only strict, freshly authorized readers may attest a complete result."""
    value: object


def _snapshot(value):
    """Typed framing preserves list order and bool/number/null distinctions."""
    output, ancestors = bytearray(), set()
    nodes = 0

    def emit(data):
        if len(output) + len(data) > MAX_RESULT_BYTES:
            raise ValueError('Tool result exceeds history byte limit')
        output.extend(data)

    def text(value):
        if len(value) > MAX_RESULT_BYTES:
            raise ValueError('Tool text exceeds history byte limit')
        data = value.encode('utf-8', errors='strict')
        emit(str(len(data)).encode() + b':' + data)

    def visit(item, depth):
        nonlocal nodes
        nodes += 1
        if depth > MAX_DEPTH or nodes > MAX_NODES:
            raise ValueError('Tool result exceeds history structure limit')
        if item is None:
            emit(b'n')
        elif type(item) is bool:
            emit(b't' if item else b'f')
        elif type(item) is str:
            emit(b's')
            text(item)
        elif type(item) is int or type(item) is Decimal:
            if type(item) is int and item.bit_length() > 127:
                raise ValueError('Tool number exceeds DynamoDB precision')
            number = Decimal(item)
            if not number.is_finite():
                raise ValueError('Tool number must be finite')
            sign, digits, exponent = number.as_tuple()
            if len(digits) > 38 or abs(exponent) > 165:
                raise ValueError('Tool number exceeds history precision/range')
            digits = ''.join(str(digit) for digit in digits)
            if not any(digit != '0' for digit in digits):
                normalized = '0'
            else:
                stripped = digits.rstrip('0')
                normalized = ('-' if sign else '') + stripped + 'e' + str(exponent + len(digits) - len(stripped))
            emit(b'd')
            text(normalized)
        elif type(item) in (dict, list):
            if id(item) in ancestors or len(item) > MAX_NODES:
                raise ValueError('Invalid tool result structure')
            ancestors.add(id(item))
            try:
                if type(item) is dict:
                    if not all(type(key) is str for key in item):
                        raise ValueError('Tool result keys must be strings')
                    emit(b'm' + str(len(item)).encode() + b':')
                    result = {}
                    for key in sorted(item):
                        text(key)
                        result[key] = visit(item[key], depth + 1)
                else:
                    emit(b'l' + str(len(item)).encode() + b':')
                    result = [visit(child, depth + 1) for child in item]
                return result
            finally:
                ancestors.remove(id(item))
        else:
            raise ValueError('Unsupported tool result type')
        return item

    copied = visit(value, 0)
    return copied, bytes(output)


def fingerprint(value):
    return hashlib.sha256(b'tool-history-v1\0' + _snapshot(value)[1]).hexdigest()


def _user(user_id):
    if type(user_id) is not str or not IDENTIFIER.fullmatch(user_id):
        raise ValueError('Current authenticated user is required')
    return user_id


def _string(value, maximum, *, required=False):
    if type(value) is not str or len(value) > maximum or len(value.encode()) > maximum:
        raise ValueError('Invalid bounded tool string')
    if required and not value.strip():
        raise ValueError('Required tool string is empty')
    return value


def normalize_input(name, arguments):
    fields = {
        'list_meetings': {'dateFrom', 'dateTo', 'tag', 'keyword', 'limit'},
        'list_accounts': set(),
        'get_account_insights': {'account', 'from', 'to', 'types'},
        'get_account_brief': {'account'},
    }
    if name not in fields or type(arguments) is not dict or set(arguments) - fields[name]:
        raise ValueError('Unsupported readonly tool or input field')
    normalized = {}
    for key, value in arguments.items():
        if value is None:
            continue
        if key == 'limit':
            if type(value) is Decimal:
                if not value.is_finite() or value != value.to_integral_value() or not 1 <= value <= 100:
                    raise ValueError('Invalid meeting limit')
                value = int(value)
            if type(value) is not int or not 1 <= value <= 100:
                raise ValueError('Invalid meeting limit')
            normalized[key] = value
        elif key == 'types':
            if (type(value) is not list or len(value) > len(INSIGHT_TYPES)
                    or any(type(item) is not str or item not in INSIGHT_TYPES for item in value)):
                raise ValueError('Invalid insight types')
            normalized[key] = list(value)
        else:
            maximum = 800 if key == 'keyword' else 1024 if key == 'account' else 128
            normalized[key] = _string(value, maximum, required=key == 'account')
    if name == 'list_meetings':
        normalized.setdefault('limit', 20)
    if name in ('get_account_insights', 'get_account_brief') and 'account' not in normalized:
        raise ValueError('Account selector is required')
    return normalized


def is_tool_dependency(dependency):
    return isinstance(dependency, dict) and (
        'readOnlyTool' in dependency or 'researchReceipt' in dependency)


def valid_tool_dependency(dependency):
    try:
        if not isinstance(dependency, dict):
            return False
        _user(dependency.get('userId'))
        revision = dependency.get('sourceRevision')
        if type(revision) is not str or not HEX_REVISION.fullmatch(revision):
            return False
        if 'readOnlyTool' in dependency:
            return (set(dependency) == {'readOnlyTool', 'toolInput', 'userId', 'sourceRevision'}
                    and normalize_input(dependency['readOnlyTool'], dependency['toolInput']) == dependency['toolInput'])
        if set(dependency) != {'researchReceipt', 'userId', 'sourceRevision'}:
            return False
        receipt = dependency['researchReceipt']
        if type(receipt) is not dict or set(receipt) != {'topic', 'mode', 'researchId'}:
            return False
        _string(receipt['topic'], 4000, required=True)
        if receipt['mode'] not in ('quick', 'standard', 'deep'):
            return False
        _user(receipt['researchId'])
        return revision == fingerprint(['research-receipt-v1', dependency['userId'], receipt])
    except (ValueError, TypeError, KeyError, UnicodeError):
        return False


def tool_dependency_key(dependency):
    if 'readOnlyTool' in dependency:
        return ('readonly-tool', dependency['userId'], dependency['readOnlyTool'],
                fingerprint(dependency['toolInput']))
    return ('research-receipt', dependency['userId'], dependency['researchReceipt']['researchId'])


def covers_tool_calls(messages, dependencies):
    """A replayable flag cannot substitute for a tracked read/creation receipt."""
    try:
        for message in messages:
            for block in message['content']:
                if type(block) is not dict:
                    return False
                if 'toolUse' not in block:
                    continue
                tool = block['toolUse']
                if type(tool) is not dict or type(tool.get('name')) is not str:
                    return False
                name, arguments = tool['name'], tool.get('input')
                if name in READONLY_TOOLS:
                    arguments = normalize_input(name, arguments)
                    if not any(dep.get('readOnlyTool') == name and dep['toolInput'] == arguments for dep in dependencies):
                        return False
                elif name == 'start_research':
                    if type(arguments) is not dict or set(arguments) - {'topic', 'mode'}:
                        return False
                    if not any('researchReceipt' in dep
                               and dep['researchReceipt']['topic'] == arguments.get('topic')
                               and dep['researchReceipt']['mode'] == arguments.get('mode', 'standard')
                               for dep in dependencies):
                        return False
        return True
    except (ValueError, TypeError, KeyError):
        return False


class ToolHistory:
    """Callbacks are trusted adapters; dependency data can never select a mutation."""
    def __init__(self, user_id, readers):
        self.user_id = _user(user_id)
        if type(readers) is not dict or set(readers) - READONLY_TOOLS or not all(callable(fn) for fn in readers.values()):
            raise ValueError('Only explicit readonly callbacks may be registered')
        self._readers = dict(readers)

    def callbacks(self, state):
        """One context.update() wires the existing tools' callback signatures."""
        def read(user_id, name, arguments):
            if user_id != self.user_id:
                state['replayable'] = False
                raise ValueError('Readonly callback user differs from current user')
            return self.read(state, name, arguments)
        return {
            'list_meetings': lambda user_id, date_from=None, date_to=None, tag=None, keyword=None, limit=None: read(
                user_id, 'list_meetings', {'dateFrom': date_from, 'dateTo': date_to, 'tag': tag,
                                         'keyword': keyword, 'limit': limit}),
            'list_accounts': lambda user_id: read(user_id, 'list_accounts', {}),
            'get_account_insights': lambda user_id, account_query, date_from=None, date_to=None, types=None: read(
                user_id, 'get_account_insights', {'account': account_query, 'from': date_from,
                                                'to': date_to, 'types': types}),
            'get_account_brief': lambda user_id, account_query: read(
                user_id, 'get_account_brief', {'account': account_query}),
        }

    def _value(self, name, arguments):
        callback = self._readers.get(name)
        if callback is None:
            raise ValueError('Readonly callback is unavailable')
        if name == 'list_meetings':
            kwargs = {target: arguments.get(source) for source, target in (
                ('dateFrom', 'date_from'), ('dateTo', 'date_to'), ('tag', 'tag'),
                ('keyword', 'keyword'), ('limit', 'limit'))}
        elif name == 'list_accounts':
            kwargs = {}
        else:
            kwargs = {'account_query': arguments['account']}
            if name == 'get_account_insights':
                kwargs.update(date_from=arguments.get('from'), date_to=arguments.get('to'),
                              types=copy_input(arguments.get('types')))
        result = callback(self.user_id, **kwargs)
        if not isinstance(result, CompleteRead):
            raise ValueError('Readonly callback did not attest a complete authorized read')
        value = result.value
        if name in ('list_meetings', 'list_accounts'):
            valid = type(value) is list
        else:
            valid = type(value) is dict and 'error' not in value and type(value.get('account')) is str
            if name == 'get_account_insights':
                valid = valid and type(value.get('insights')) is list
            else:
                valid = (valid and type(value.get('insightsByType')) is dict
                         and type(value.get('meetings')) is list and type(value.get('research')) is list)
        if not valid:
            raise ValueError('Readonly callback returned an invalid/error result')
        return _snapshot(value)[0]

    @staticmethod
    def _capacity(state, key):
        dependencies = [dep for dep in state['dependencies'] if is_tool_dependency(dep)]
        if len(dependencies) >= MAX_TOOL_DEPENDENCIES and not any(tool_dependency_key(dep) == key for dep in dependencies):
            raise ValueError('Readonly history dependency budget exceeded')

    def read(self, state, name, arguments):
        from session_provenance import remember_source
        try:
            arguments = normalize_input(name, arguments)
            dependency = {'readOnlyTool': name, 'toolInput': arguments, 'userId': self.user_id,
                          'sourceRevision': '0' * 64}
            self._capacity(state, tool_dependency_key(dependency))
            value = self._value(name, arguments)
            dependency['sourceRevision'] = fingerprint(['readonly-tool-v1', self.user_id, name, arguments, value])
            remember_source(state, dependency)
            return value
        except Exception:
            state['replayable'] = False
            raise

    def research_receipt(self, state, arguments, result):
        from session_provenance import remember_source
        try:
            if type(arguments) is not dict or set(arguments) - {'topic', 'mode'}:
                raise ValueError('Invalid research receipt input')
            if type(result) is not dict or set(result) != {'researchId'}:
                raise ValueError('Only a successful creation ID can become a receipt')
            receipt = {'topic': _string(arguments.get('topic'), 4000, required=True),
                       'mode': arguments.get('mode', 'standard'), 'researchId': _user(result['researchId'])}
            dependency = {'researchReceipt': receipt, 'userId': self.user_id,
                          'sourceRevision': fingerprint(['research-receipt-v1', self.user_id, receipt])}
            if not valid_tool_dependency(dependency):
                raise ValueError('Invalid research receipt')
            self._capacity(state, tool_dependency_key(dependency))
            remember_source(state, dependency)
            return dict(receipt)
        except Exception:
            state['replayable'] = False
            raise

    def is_current(self, dependency):
        if not valid_tool_dependency(dependency) or dependency['userId'] != self.user_id:
            return False
        if 'researchReceipt' in dependency:
            return True  # Immutable creation receipt; never read mutable research or invoke creation.
        try:
            name = dependency['readOnlyTool']
            arguments = normalize_input(name, dependency['toolInput'])
            value = self._value(name, arguments)
            return dependency['sourceRevision'] == fingerprint(['readonly-tool-v1', self.user_id, name, arguments, value])
        except Exception:
            return False  # Read/auth errors invalidate the entire prior conversation.


def copy_input(value):
    return list(value) if value is not None else None
