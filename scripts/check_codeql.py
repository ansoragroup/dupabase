#!/usr/bin/env python3
"""Gate the analyzed GitHub ref on open security alerts; keep review in GitHub."""

import json
import os
import subprocess


repository = os.environ["GITHUB_REPOSITORY"]
ref = os.environ["GITHUB_REF"]
language = os.environ["CODEQL_LANGUAGE"]
prefix = "go/" if language == "go" else "js/"
result = subprocess.run(
    ["gh", "api", "--method", "GET", "--paginate", "--slurp",
     f"repos/{repository}/code-scanning/alerts", "-f", "state=open", "-f", f"ref={ref}"],
    check=True, capture_output=True, text=True,
)
alerts = [alert for page in json.loads(result.stdout) for alert in page
          if alert["tool"]["name"] == "CodeQL"
          and alert["rule"]["id"].startswith(prefix)
          and alert["rule"].get("security_severity_level")]
for alert in alerts:
    location = alert["most_recent_instance"]["location"]
    print(f"Open security alert #{alert['number']}: {alert['rule']['id']} "
          f"at {location['path']}:{location['start_line']}")
print(f"CodeQL {language}: {len(alerts)} open security alerts on {ref}")
raise SystemExit(bool(alerts))
