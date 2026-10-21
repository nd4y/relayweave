# Contributing

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

Update English/Russian README and relevant documentation together. Preserve
third-party notices and artifact verification. Do not equate a passing unit test
with live proxy, UDP, throughput, recovery or production acceptance.
