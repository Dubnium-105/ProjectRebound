#!/usr/bin/env python3
"""Assemble explicitly staged hardware-test files, never a production release."""
import argparse
import hashlib
import json
import re
import shutil
import tarfile
import zipfile
from datetime import datetime, timezone
from pathlib import Path, PurePosixPath


def sha(path):
    digest = hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b''):
            digest.update(block)
    return digest.hexdigest()


def read(path):
    return json.loads(path.read_text(encoding='utf-8-sig'))


def checked_inputs(stage):
    """Require a small, explicit staging tree and reject sensitive file types."""
    result = []
    allowed = {'.exe', '.dll', '.md', '.txt', '.json', '.example', '.ps1',
               '.cmd', '.sh', '.yaml', '.yml', '.toml', '.pem', '.sql'}
    # Crypto libraries embed PEM delimiter strings in executables. A delimiter
    # without a following encoded body is not private key material.
    secret_pem = re.compile(rb'-----BEGIN (?:[A-Z ]+ )?PRIVATE KEY-----[\r\n]+'
                            rb'[A-Za-z0-9+/=\r\n ]{64,}'
                            rb'-----END (?:[A-Z ]+ )?PRIVATE KEY-----')
    for path in sorted(stage.rglob('*')):
        if path.is_symlink():
            raise ValueError('Symlinks are not distribution inputs: ' + str(path))
        if not path.is_file():
            continue
        relative = path.relative_to(stage)
        lowered = [part.lower() for part in relative.parts]
        if any(part in {'node_modules', 'target', '.git', '.env', 'clientlogs', 'logs'}
               or 'private' in part or part.endswith(('.dump', '.pfx', '.p12', '.key', '.dpapi'))
               for part in lowered):
            raise ValueError('Private/local state is not a distribution input: ' + str(relative))
        if path.suffix.lower() not in allowed and relative.as_posix() not in {
                'bin/control-plane', 'bin/meta-server', 'bin/edge-relay', 'SHA256SUMS'}:
            raise ValueError('Unreviewed distribution file: ' + str(relative))
        raw = path.read_bytes()
        if secret_pem.search(raw) or (path.suffix.lower() == '.pem' and
                re.search(rb'-----BEGIN (?:[A-Z ]+ )?PRIVATE KEY-----', raw)):
            raise ValueError('A private key was found in a distribution input: ' + str(relative))
        if path.suffix.lower() not in {'.exe', '.dll'} and not raw.startswith(b'\x7fELF'):
            # Full runtime tokens are never needed in an operator template.
            if re.search(rb'eyJ[A-Za-z0-9_-]{15,}\.[A-Za-z0-9_-]{15,}\.[A-Za-z0-9_-]{15,}', raw):
                raise ValueError('JWT-shaped value in distribution input: ' + str(relative))
        result.append((path, relative))
    if not result:
        raise ValueError('Empty distribution staging directory')
    return result


def build(stage, destination, metadata, archive_kind):
    inputs = checked_inputs(stage)
    expected_artifacts = metadata['required_artifacts'][metadata['package_role']]
    declared = {entry['path']:entry for entry in expected_artifacts}
    if len(declared) != len(expected_artifacts) or not declared:
        raise ValueError('Missing or duplicate artifact pins')
    actual_binaries = {relative.as_posix() for path, relative in inputs
                       if path.suffix.lower() in {'.exe','.dll'} or relative.parts[0]=='bin'}
    if actual_binaries != set(declared):
        raise ValueError('Staged executables differ from the explicit artifact allowlist')
    for name, entry in declared.items():
        relative = PurePosixPath(name)
        if relative.is_absolute() or '..' in relative.parts or '\\' in name:
            raise ValueError('Artifact path must stay inside its package')
        source = stage/name
        if sha(source) != entry['sha256'] or source.stat().st_size != entry['bytes']:
            raise ValueError('Staged artifact differs from its reviewed build: '+name)
    destination.mkdir(parents=True, exist_ok=False)
    for source, relative in inputs:
        target = destination/relative
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source, target)
        if sha(source) != sha(target):
            raise ValueError('Distribution input changed while copying: ' + str(source))
    manifest_path = destination/'package-manifest.json'
    if manifest_path.exists():
        raise ValueError('Staging must not contain a pre-generated package manifest')
    records = [{'path':p.relative_to(destination).as_posix(), 'bytes':p.stat().st_size, 'sha256':sha(p)}
               for p in sorted(destination.rglob('*')) if p.is_file()]
    manifest = dict(metadata, files=records)
    # ASCII JSON also round-trips through Windows PowerShell 5.1's default
    # Get-Content encoding, while decoded values retain their Unicode content.
    manifest_path.write_text(json.dumps(manifest, ensure_ascii=True, indent=2)+'\n', encoding='utf-8')
    checksums = destination/'SHA256SUMS'
    if checksums.exists():
        raise ValueError('Staging must not contain pre-generated checksums')
    checksums.write_text(''.join(f'{sha(p)}  {p.relative_to(destination).as_posix()}\n'
                                 for p in sorted(destination.rglob('*')) if p.is_file()), encoding='utf-8')
    expected = {p.relative_to(destination).as_posix():sha(p) for p in destination.rglob('*') if p.is_file()}
    archive = destination.with_suffix('.zip' if archive_kind=='zip' else '.tar.gz')
    if archive.exists():
        raise ValueError('Refuse to overwrite an earlier package')
    if archive_kind == 'zip':
        with zipfile.ZipFile(archive, 'x', compression=zipfile.ZIP_DEFLATED, compresslevel=6) as output:
            for name in sorted(expected):
                output.write(destination/name, destination.name+'/'+name)
        with zipfile.ZipFile(archive) as check:
            contents = {str(PurePosixPath(i.filename).relative_to(destination.name)):check.read(i)
                        for i in check.infolist() if not i.is_dir()}
    else:
        with tarfile.open(archive, 'x:gz', compresslevel=6) as output:
            for name in sorted(expected):
                path = destination/name
                info = output.gettarinfo(str(path), arcname=destination.name+'/'+name)
                info.uid = info.gid = 0
                info.uname = info.gname = ''
                info.mode = 0o755 if name.startswith('bin/') or name.endswith('.sh') else 0o644
                with path.open('rb') as stream:
                    output.addfile(info, stream)
        with tarfile.open(archive, 'r:gz') as check:
            contents = {str(PurePosixPath(i.name).relative_to(destination.name)):check.extractfile(i).read()
                        for i in check.getmembers() if i.isfile()}
    if set(contents) != set(expected):
        raise ValueError('Archive file set differs from staged package')
    for name, value in contents.items():
        if hashlib.sha256(value).hexdigest() != expected[name]:
            raise ValueError('Archive byte mismatch: ' + name)
    return {'path':str(archive), 'sha256':sha(archive), 'bytes':archive.stat().st_size,
            'files':len(expected), 'archive_byte_check':'PASS',
            'manifest_sha256':sha(manifest_path), 'staging_directory':str(destination)}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--windows-stage', type=Path, required=True)
    parser.add_argument('--server-stage', type=Path)
    parser.add_argument('--metadata', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    metadata = read(args.metadata)
    if metadata.get('purpose') != 'hardware-test-only' or metadata.get('release_ready') is not False:
        raise ValueError('Only a hardware test candidate can be packaged by this driver')
    if metadata.get('schema_version') != 1 or not metadata.get('source_commits'):
        raise ValueError('Package metadata must identify the actual build sources')
    commits = metadata['source_commits']
    if not isinstance(commits, dict) or set(commits) != {'ProjectRebound','Toolbox'} or any(
            not isinstance(value, str) or not re.fullmatch(r'[0-9a-f]{40}', value) for value in commits.values()):
        raise ValueError('Both source commits must be complete lowercase Git object IDs')
    game = metadata.get('pinned_game', {})
    if game.get('sha256') != '181c49ffb522b3eb01014c84fd9d3a2a5c0b66ae80a6a6addff4bdd6f8125843' or game.get('bytes') != 102362112:
        raise ValueError('Package must target the reviewed fixed Boundary executable')
    compiler = metadata.get('msvc_minimum_version', '')
    if not re.fullmatch(r'[0-9]+(?:\.[0-9]+){2,3}', compiler) or tuple(map(int, compiler.split('.'))) < (14,50,35717):
        raise ValueError('Package must specify a runtime minimum at least as recent as the actual MSVC build tools')
    if not isinstance(metadata.get('required_artifacts'), dict):
        raise ValueError('Explicit artifact pins are required')
    package_id = metadata.get('package_id', '')
    if not re.fullmatch(r'[A-Za-z0-9_-]{1,80}', package_id):
        raise ValueError('Invalid package ID')
    args.output.mkdir(parents=True, exist_ok=False)
    packages = []
    stages = [('windows-x64',args.windows_stage,'zip')]
    if args.server_stage is not None:
        stages.append(('server-linux-amd64',args.server_stage,'tar'))
    for role, stage, extension in stages:
        packages.append(build(stage.resolve(strict=True),args.output/(package_id+'-'+role),
                              dict(metadata, package_role=role),extension))
    receipt = {'recorded_at':datetime.now(timezone.utc).isoformat(),
               'status':'PASS_PACKAGE_BYTES_ONLY', 'release_ready':False,
               'scope':'Exact package bytes checked by reading every archive; not remote installation or native gameplay acceptance.',
               'builder_sha256':sha(Path(__file__)), 'metadata_sha256':sha(args.metadata),
               'packages':packages}
    (args.output/'package-build-receipt.json').write_text(json.dumps(receipt,indent=2)+'\n',encoding='utf-8')
    (args.output/'SHA256SUMS').write_text(''.join(f'{item["sha256"]}  {Path(item["path"]).name}\n'
                                                for item in packages),encoding='utf-8')
    print(json.dumps(receipt))


if __name__ == '__main__':
    main()
