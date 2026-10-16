"""Read-only publication evidence shared by source notifications and discovery."""
from __future__ import annotations

import json
import os
import re
import subprocess
from pathlib import Path

PUBLISHERS = {
    "echovisionlab/geul-web": {"Publish Image"},
    "echovisionlab/geul-api": {"Publish Image"},
    "echovisionlab/geul-editor-collab": {"Publish Image"},
    "echovisionlab/geul-postgres": {"Publish Image"},
    "echovisionlab/geul-identity": {
        "Publish Kratos image", "Publish MCP OAuth facade image", "Publish Oathkeeper image",
    },
    "echovisionlab/geul-common": {"Publish GitHub Packages npm package"},
    "echovisionlab/geul-event-contracts": {"Publish GitHub Packages"},
    "echovisionlab/geul-telemetry": {"Publish npm package"},
    "echovisionlab/geul-schema": set(),
    "echovisionlab/geul-observability": set(),
}
TOOL_PUBLISHERS = {f"publish ({tool})" for tool in ("transcode", "youtube-audio", "hwp", "portadj")}


def publisher_jobs_succeeded(repository: str, sha: str, run: dict, jobs: list[dict]) -> bool:
    if not (run["head_sha"] == sha and run["head_branch"] == "main" and run["status"] == "completed" and run["conclusion"] == "success"):
        return False
    publishers = [job for job in jobs if job["name"].lower().startswith("publish")]
    allowed = TOOL_PUBLISHERS if repository == "echovisionlab/geul-tools" else PUBLISHERS.get(repository, set())
    # A rerun-all can succeed after Release Please returns release_created=false.
    # Its skipped matrix placeholder cannot borrow a prior attempt's success.
    if any(job["run_attempt"] == run["run_attempt"] and (job["conclusion"] != "success" or job["name"] not in allowed) for job in publishers):
        return False
    latest = {}
    for job in publishers:
        previous = latest.get(job["name"])
        if previous is None or (job["run_attempt"], job["id"]) > (previous["run_attempt"], previous["id"]):
            latest[job["name"]] = job
    expected = TOOL_PUBLISHERS.intersection(latest) if repository == "echovisionlab/geul-tools" else allowed
    return bool(expected) and all(
        name in latest
        and latest[name]["run_id"] == run["id"]
        and latest[name]["head_sha"] == sha
        and latest[name]["status"] == "completed"
        and latest[name]["conclusion"] == "success"
        for name in expected
    )


def read_github(path: str) -> dict:
    result = subprocess.run(["gh", "api", path], capture_output=True, text=True)
    if result.returncode:
        raise ValueError("Unable to read publication evidence metadata")
    return json.loads(result.stdout)


def read_jobs(repository: str, run_id: int, github=read_github) -> list[dict]:
    jobs = []
    page = 1
    while True:
        values = github(f"repos/{repository}/actions/runs/{run_id}/jobs?filter=all&per_page=100&page={page}")["jobs"]
        jobs.extend(values)
        if len(values) < 100:
            return jobs
        page += 1


def main() -> None:
    repository = os.environ["GITHUB_REPOSITORY"]
    sha = os.environ["RELEASE_HEAD_SHA"]
    run_id = os.environ["RELEASE_RUN_ID"]
    if repository not in PUBLISHERS and repository != "echovisionlab/geul-tools":
        raise ValueError("Source repository is not registered")
    if not re.fullmatch(r"[a-f0-9]{40}", sha) or not re.fullmatch(r"[0-9]+", run_id):
        raise ValueError("Invalid source release event metadata")
    run = read_github(f"repos/{repository}/actions/runs/{run_id}")
    release = read_github(f"repos/{repository}/releases/latest")
    tagged = (
        run["head_sha"] == sha and run["head_branch"] == "main" and run["event"] == "push"
        and run["status"] == "completed" and run["conclusion"] == "success"
        and release["target_commitish"] == sha
    )
    published = tagged and publisher_jobs_succeeded(repository, sha, run, read_jobs(repository, int(run_id)))
    with Path(os.environ["GITHUB_OUTPUT"]).open("a") as output:
        output.write(f"tagged_release={str(tagged).lower()}\n")
        output.write(f"published={str(published).lower()}\n")
    print(f"Tagged release matches completed source run: {tagged}; actual publisher evidence: {published}")


if __name__ == "__main__":
    main()
