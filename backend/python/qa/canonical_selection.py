"""Exact canonical identity selection; IDs never confer source access."""
from source_revision import IDENTIFIER, projection_identity, normalize_bindings


def selected_resource_ids(values):
    if (type(values) is not list or not 1 <= len(values) <= 5
            or any(type(value) is not str or not IDENTIFIER.fullmatch(value) for value in values)):
        raise ValueError('Select one to five canonical resource IDs')
    return sorted(set(values))


def retrieve_selected(reader, user_id, query, selected, identities, lookup):
    # Local import keeps request-history validation independent of hydration.
    from indexed_retrieval import hydrate_candidates, _current_result

    targets = {}
    for key, identity in sorted(identities.items()):
        rid = identity['resourceId']
        if rid not in selected:
            continue
        snapshot = reader.read(user_id, *key)
        if snapshot is None:
            continue
        if rid in targets:
            raise ValueError('Ambiguous authorized canonical resource ID')
        targets[rid] = (key, identity, snapshot)

    results = []
    for rid in selected:
        if rid not in targets:
            continue  # Missing and unauthorized selections expose no content.
        key, identity, before = targets[rid]
        source_filter = {'andAll': [
            {'equals': {'key': 'indexSchema', 'value': 'canonical-v1'}},
            {'equals': {'key': 'sourcePK', 'value': key[0]}},
            {'equals': {'key': 'sourceSK', 'value': key[1]}},
            {'equals': {'key': 'sourceRevision', 'value': before['revision']}},
        ]}
        candidates = []
        for candidate in lookup(source_filter):
            projected = projection_identity(candidate.get('uri', ''),
                                            candidate.get('metadata'), reader.kb_bucket)
            if (projected is not None and projected['sourcePK'] == key[0]
                    and projected['sourceSK'] == key[1]
                    and projected['indexRevision'] == before['revision']
                    and projected['filename'] in before['filenames']
                    and projected['bindings'] == normalize_bindings(before['objects'])):
                candidates.append(candidate)
        hydrated = hydrate_candidates(reader, user_id, query, candidates, {key: identity}, 1)
        current = reader.read(user_id, *key)
        if current is None or current['revision'] != before['revision']:
            raise RuntimeError('Selected source changed during retrieval')
        result = hydrated[0] if hydrated else _current_result(
            current, 'ttobak://source/' + identity['resourceHash'], 0.0, 'current_saved_selected')
        if result['dependency']['sourceRevision'] != current['revision']:
            raise RuntimeError('Selected source changed during hydration')
        if result.get('provenance', {}).get('filePending'):
            # Do not replay a temporary lack of indexed file content as a stable fact.
            result['volatile'] = True
        results.append(result)
    return results
