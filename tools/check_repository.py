"""Small credential-free repository checks. Heavy acceptance runs on a build host."""
import ast
import sys
from pathlib import Path
sys.dont_write_bytecode = True
from check_documentation import check_documentation
root = Path(__file__).resolve().parents[1]
documentation_pairs = check_documentation(root)
required = ["README.md", "README.ru.md", "LICENSE", "NOTICE", "SECURITY.md", "compose.yaml", ".env.example", "go.mod", "web/package-lock.json", "docs/images/traffic-map.png"]
for name in required:
    assert (root / name).is_file(), f"Missing {name}"
files = [p for p in root.rglob("*") if p.is_file() and ".git" not in p.relative_to(root).parts and "node_modules" not in p.relative_to(root).parts]
for p in files:
    rel = p.relative_to(root)
    assert not (p.name.startswith(".env") and p.name != ".env.example"), f"Environment file: {rel}"
    assert not any(v in {"data", ".secrets", "reports", "checkpoints", "node_modules"} for v in rel.parts), f"Local state: {rel}"
    assert ".private." not in p.name, f"Private file: {rel}"
    if p.suffix == ".py":
        ast.parse(p.read_text(encoding="utf-8"), filename=str(rel))
print(f"Repository hygiene and Python syntax passed ({len(files)} files; {documentation_pairs} bilingual documentation pairs)")
