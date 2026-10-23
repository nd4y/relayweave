# Contributing

**English** | [Русский](CONTRIBUTING.ru.md)

Use a feature branch and a pull request. Describe the user-visible problem,
resulting behavior and checks. Keep changes focused; never commit filled env
files, provider links/device identifiers, databases, certificates/private keys,
client exports, private reports or production screenshots.

Run heavy builds/tests on your build server, in an isolated project with bounded
resources. Never run fault/load fixtures against an existing production stack.
`tools/check-buildhost.sh` runs Go race/vet checks and the frontend build; browser
acceptance additionally needs a disposable control-plane/worker/origin fixture.
The lightweight GitHub check only verifies repository hygiene and Python syntax;
it is not a replacement for runtime or browser acceptance.

Maintain every project document in English (`name.md`) and Russian
(`name.ru.md`), including integration guides, security guidance and changelogs.
Update both versions in the same pull request and link them in the language
navigation and documentation index. Keep code blocks, parameter values,
implementation status and acceptance limits equivalent. LICENSE and upstream
license texts remain verbatim. Run `python3 tools/check_repository.py`; PR CI
also checks that edits, additions and deletions include both language files. Preserve
third-party notices and artifact verification. Do not equate a passing unit test
with live proxy, UDP, throughput, recovery or production acceptance.
