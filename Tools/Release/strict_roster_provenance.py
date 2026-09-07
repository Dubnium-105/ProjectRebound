#!/usr/bin/env python3
"""Collect build provenance without executing artifacts; refuse incomplete releases."""
import argparse
import hashlib
import json
import subprocess
from pathlib import Path
from datetime import datetime, timezone


def run(*args, cwd=None):
    result = subprocess.run(args, cwd=cwd, text=True, encoding="utf-8", errors="replace", capture_output=True)
    return {"command": list(args), "exit_code": result.returncode, "output": result.stdout.strip(), "error": result.stderr.strip()}


def sha(path):
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        while chunk := stream.read(1024 * 1024):
            digest.update(chunk)
    return digest.hexdigest()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=Path, required=True)
    parser.add_argument("--toolbox-repo", type=Path)
    parser.add_argument("--artifact", type=Path, action="append", default=[])
    parser.add_argument("--acceptance-report", type=Path)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--require-release-ready", action="store_true")
    args = parser.parse_args()
    problems = []
    repos = {}
    for name, repo in [("ProjectRebound", args.repo), ("Toolbox", args.toolbox_repo)]:
        if repo is None:
            problems.append(name + " source repository missing")
            continue
        repo = repo.resolve(strict=True)
        head = run("git", "rev-parse", "HEAD", cwd=repo)
        state = run("git", "status", "--porcelain", "--untracked-files=no", cwd=repo)
        repos[name] = {"path": str(repo), "commit": head["output"], "dirty": bool(state["output"]), "git_exit_codes": [head["exit_code"], state["exit_code"]]}
        if head["exit_code"] or state["exit_code"] or state["output"]:
            problems.append(name + " source provenance is not a clean committed tree")
    artifacts = []
    components = {}
    for given in args.artifact:
        path = given.resolve(strict=True)
        build_info = run("go", "version", "-m", str(path))
        artifact = {"path": str(path), "size": path.stat().st_size, "sha256": sha(path), "go_build_info": build_info}
        if path.name == "matchserver":
            artifact["release_eligible"] = False
            artifact["reason"] = "historical tracked binary excluded from all current source-built containers"
            problems.append("historical matchserver artifact cannot be released")
        else:
            artifact["release_eligible"] = None
            artifact["reason"] = "hash collection alone does not prove signature, compatibility, or native acceptance"
        for line in build_info["output"].splitlines():
            parts = line.strip().split()
            if len(parts) >= 3 and parts[0] == "dep":
                name, version = parts[1:3]
                components[(name, version)] = {"type": "library", "name": name, "version": version, "purl": "pkg:golang/" + name + "@" + version}
        artifacts.append(artifact)
    if not artifacts:
        problems.append("no release artifacts supplied")
    report = None
    if args.acceptance_report:
        path = args.acceptance_report.resolve(strict=True)
        result = json.loads(path.read_text(encoding="utf-8-sig"))
        report = {"path": str(path), "sha256": sha(path)}
        tests = result.get("component_tests", []) + result.get("e2e_tests", [])
        if len(result.get("component_tests", [])) != 52 or len(result.get("e2e_tests", [])) != 22:
            problems.append("acceptance report must contain all 52 component and 22 E2E cases")
        for test in tests:
            if test.get("status", "").lower() not in ("passed", "pass"):
                problems.append(str(test.get("id")) + " is not passed")
            if not test.get("result"):
                problems.append(str(test.get("id")) + " has no recorded evidence")
        expected_commits = result.get("commit_pair", {})
        for name, repo in repos.items():
            if expected_commits.get(name) != repo["commit"]:
                problems.append(name + " acceptance commit does not match the checked out source")
        attested_artifacts = {item.get("sha256"): item for item in result.get("artifact_hashes", []) if isinstance(item, dict)}
        for artifact in artifacts:
            attestation = attested_artifacts.get(artifact["sha256"], {})
            if attestation.get("signature_status") != "PASS" or attestation.get("compatibility_status") != "PASS":
                problems.append(Path(artifact["path"]).name + " has no matching verified artifact/signature/compatibility evidence")
            elif artifact["release_eligible"] is not False:
                artifact["release_eligible"] = True
                artifact["reason"] = "exact digest linked to supplied verified acceptance evidence"
        if result.get("release_ready") is not True:
            problems.append("acceptance report has not established release readiness")
    else:
        problems.append("native acceptance report missing")
    for artifact in artifacts:
        if artifact["release_eligible"] is not True:
            problems.append(Path(artifact["path"]).name + " is not an attested release artifact")
    output = args.output.resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    manifest = {"schema_version": 1, "generated_at": datetime.now(timezone.utc).isoformat(), "mode": "read_only_provenance", "repositories": repos,
                "artifacts": artifacts, "acceptance_report": report, "release_ready": not problems, "blockers": problems}
    output.write_text(json.dumps(manifest, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    sbom = {"bomFormat": "CycloneDX", "specVersion": "1.5", "version": 1, "components": list(components.values())}
    output.with_suffix(".sbom.json").write_text(json.dumps(sbom, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(json.dumps({"output": str(output), "artifacts": len(artifacts), "release_ready": not problems, "blocker_count": len(problems)}))
    return 3 if args.require_release_ready and problems else 0


if __name__ == "__main__":
    raise SystemExit(main())
