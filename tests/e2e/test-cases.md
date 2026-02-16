# NEREID Prompt-Variation E2E Cases (Playwright-CLI)

Target: `https://nereid.yuiseki.net/`

Each case verifies this flow on the real site:
1. Open top page (`/`).
2. Submit prompt from UI.
3. Redirect to `/works/<workId>`.
4. `workId` format is valid.
5. `/api/status/<workId>` is reachable and returns known phase.

## Prompt Cases (10)

1. `東京都台東区のラーメン屋を表示してください。`
2. `東京都文京区の公園を表示してください。`
3. `東京都渋谷区の図書館を表示してください。`
4. `東京都千代田区の交番を表示してください。`
5. `東京都港区の病院を表示してください。`
6. `東京都墨田区のコンビニを表示してください。`
7. `東京都江東区の公園を表示してください。`
8. `東京都新宿区の駅を表示してください。`
9. `東京都台東区と東京都文京区の公園を色分けして表示してください。`
10. `東京都台東区の公園を表示して、クリックで名前を表示してください。`

Execution:
- `./tests/e2e/run-playwright-cli-e2e.sh`

Result artifacts:
- `tests/results/<run-id>/summary.tsv`
- `tests/results/<run-id>/summary.md`
- `tests/results/<run-id>/<case-id>.log`
- `tests/results/<run-id>/<case-id>.result.json`
