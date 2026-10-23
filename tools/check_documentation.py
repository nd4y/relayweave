"""Check bilingual first-party documentation without network or dependencies."""
import argparse
from pathlib import Path, PurePosixPath
import re
import subprocess
from urllib.parse import unquote, urlsplit


def is_documentation(path):
    parts = PurePosixPath(path).parts
    return bool(parts) and path.endswith('.md') and (
        len(parts) == 1 or parts[0] in {'docs', 'integrations'}
    )


def counterpart(path):
    return path[:-6] + '.md' if path.endswith('.ru.md') else path[:-3] + '.ru.md'


def prose(text):
    return re.sub(r'^```[^\n]*\n.*?^```[^\n]*$', '', text, flags=re.M | re.S)


def code_blocks(text):
    return re.findall(r'^```[^\n]*\n(.*?)^```[^\n]*$', text, flags=re.M | re.S)


def check_documentation(root):
    root = root.resolve()
    docs = sorted(p for p in root.rglob('*.md')
                  if not any(v in p.relative_to(root).parts for v in ('.git', 'node_modules'))
                  and is_documentation(p.relative_to(root).as_posix()))
    for path in docs:
        rel = path.relative_to(root).as_posix()
        other = root / counterpart(rel)
        if not other.is_file():
            raise ValueError(f'Missing language counterpart: {rel} -> {counterpart(rel)}')
        text = path.read_text(encoding='utf-8')
        content = prose(text)
        if f']({other.name})' not in '\n'.join(content.splitlines()[:6]):
            raise ValueError(f'Missing language navigation: {rel}')
        for target in re.findall(r'\]\(([^)]+)\)', content):
            url = urlsplit(target)
            if url.scheme or url.netloc or not url.path:
                continue
            resolved = (path.parent / unquote(url.path)).resolve()
            if not resolved.is_relative_to(root) or not resolved.exists():
                raise ValueError(f'Broken local link: {rel} -> {target}')
            if rel.endswith('.ru.md') and url.path.endswith('.md') and resolved != other:
                if not url.path.endswith('.ru.md'):
                    raise ValueError(f'Russian page links to English page: {rel} -> {target}')
        if rel.endswith('.ru.md') and rel != 'README.ru.md':
            english = other.read_text(encoding='utf-8')
            if code_blocks(text) != code_blocks(english):
                raise ValueError(f'Translated command/code block differs: {rel}')
            levels = lambda value: re.findall(r'^(#{1,6}) ', prose(value), flags=re.M)
            if levels(text) != levels(english):
                raise ValueError(f'Section structure differs: {rel}')
    return len(docs) // 2


def check_changed_pairs(paths):
    changed = set(paths)
    for path in sorted(changed):
        if is_documentation(path) and counterpart(path) not in changed:
            raise ValueError(f'Update both language files in this PR: {path} and {counterpart(path)}')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--base', help='PR base commit for paired change/add/delete checks')
    args = parser.parse_args()
    root = Path(__file__).resolve().parents[1]
    pairs = check_documentation(root)
    if args.base:
        result = subprocess.check_output([
            'git', '-C', str(root), 'diff', '--name-only', '--no-renames', '-z',
            args.base, 'HEAD', '--',
        ])
        check_changed_pairs(result.decode('utf-8').strip('\0').split('\0'))
    print(f'Bilingual documentation passed ({pairs} English/Russian pairs)')


if __name__ == '__main__':
    main()
