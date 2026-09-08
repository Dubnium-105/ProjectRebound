import re

_OWNERS = ('ProjectRebound', 'Toolbox')
_COMMIT = re.compile(r'^[0-9a-fA-F]{40}$')


def _complete_pair(value):
    """Return a complete recorded pair, or None.

    A ledger must never fill in the other repository from the current checkout.
    In particular, a historical report containing only ``toolbox_commit`` is an
    incomplete execution snapshot, even when its value happens to be an ancestor
    of the candidate being reviewed.
    """
    if not isinstance(value, dict):
        return None
    if set(value) != set(_OWNERS):
        return None
    if not all(isinstance(value.get(owner), str) and _COMMIT.fullmatch(value[owner]) for owner in _OWNERS):
        return None
    return {owner: value[owner].lower() for owner in _OWNERS}


def execution_pair_from_report(report):
    """Read only the source pair recorded by an execution report.

    ``source_pair`` is preferred, but the explicit execution aliases are also
    accepted for reports produced by the E2E drivers.  No current HEAD or
    reviewed pair is ever substituted for a missing owner.
    """
    nested = report.get('result') if isinstance(report.get('result'), dict) else {}
    for candidate in (
        report.get('execution_source_pair'),
        report.get('execution_pair'),
        nested.get('execution_source_pair'),
        nested.get('execution_pair'),
        report.get('source_pair'),
    ):
        pair = _complete_pair(candidate)
        if pair is not None:
            return pair
        if (isinstance(candidate, dict) and candidate and set(candidate).issubset(_OWNERS)
                and all(isinstance(commit, str) and _COMMIT.fullmatch(commit) for commit in candidate.values())):
            # An explicit one-repository execution is still a real source fact.
            # Preserve it before considering less-specific historical aliases;
            # never supplement it with the current other repository.
            return {owner: commit.lower() for owner, commit in candidate.items()}

    # Historical reports recorded only the repository actually exercised.  Keep
    # that incomplete fact rather than manufacturing the other repository.
    pair = report.get('source_pair')
    if isinstance(pair, dict):
        legacy = {}
        if isinstance(pair.get('toolbox_commit'), str) and _COMMIT.fullmatch(pair['toolbox_commit']):
            legacy['Toolbox'] = pair['toolbox_commit'].lower()
        if isinstance(pair.get('project_rebound_commit'), str) and _COMMIT.fullmatch(pair['project_rebound_commit']):
            legacy['ProjectRebound'] = pair['project_rebound_commit'].lower()
        if legacy:
            return legacy
    if report.get('id') == 'E2E-19' and isinstance(report.get('source_commit'), str):
        return {'ProjectRebound': report['source_commit']}
    return None


def execution_source_snapshot(report):
    """Extract the immutable execution-source facts without relabeling them.

    The resulting object deliberately carries ``historical_only``.  It is
    suitable for retaining alongside a final reviewed source pair, but it must
    not be used as proof that the final candidate was executed.  Reports made
    before source metadata was standardized can have an incomplete pair; that
    is represented explicitly as ``execution_pair_complete: false``.
    """
    nested = report.get('result') if isinstance(report.get('result'), dict) else {}
    pair = execution_pair_from_report(report)

    def first(*names):
        for name in names:
            if name in report and report[name] is not None:
                return report[name]
            if name in nested and nested[name] is not None:
                return nested[name]
        return None

    source_pair_metadata = report.get('source_pair') if isinstance(report.get('source_pair'), dict) else {}
    dirty = first('source_dirty', 'tree_dirty', 'toolbox_tree_dirty', 'project_rebound_tree_dirty')
    if dirty is None:
        dirty = first('source_dirty_by_repository')
    if dirty is None:
        # E2E-20 uses repository-specific dirty flags. Preserve them in a
        # structured field rather than collapsing them into a false clean flag.
        dirty_keys = {'ProjectRebound':'project_rebound_tree_dirty', 'Toolbox':'toolbox_tree_dirty'}
        dirty = {
            owner: nested.get(dirty_keys[owner])
            for owner in _OWNERS
            if dirty_keys[owner] in nested
        } or None
    if dirty is None:
        dirty_keys = {'ProjectRebound':'project_rebound_tree_dirty', 'Toolbox':'toolbox_tree_dirty'}
        dirty = {
            owner: source_pair_metadata.get(dirty_keys[owner])
            for owner in _OWNERS
            if dirty_keys[owner] in source_pair_metadata
        } or None

    return {
        'source_pair': pair,
        'execution_source_pair': pair,
        'source_commit': first('source_commit'),
        'source_input_commit': first('source_input_commit', 'build_source_commit'),
        'source_sha256': first('source_sha256', 'source_digest', 'build_sha256'),
        'source_state': first('source_state'),
        'source_dirty': dirty,
        'source_tree': first('source_tree', 'git_tree', 'source_subtree'),
        'source_files': first('source_files', 'source_file_hashes', 'changed_files'),
        'execution_pair_complete': _complete_pair(pair) is not None,
        'historical_only': True,
    }
