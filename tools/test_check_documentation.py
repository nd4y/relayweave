"""Lightweight regression checks for documentation publication guards."""
from pathlib import Path
import tempfile
import unittest

from check_documentation import check_changed_pairs, check_documentation


class DocumentationChecks(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name)
        (self.root / 'docs').mkdir()
        self.english = self.root / 'docs/guide.md'
        self.russian = self.root / 'docs/guide.ru.md'
        self.en = '# Guide\n\n[Russian](guide.ru.md)\n\n## Run\n\n```sh\napp --limit=4\n```\n'
        self.ru = '# Руководство\n\n[English](guide.md)\n\n## Запуск\n\n```sh\napp --limit=4\n```\n'
        self.english.write_text(self.en, encoding='utf-8')
        self.russian.write_text(self.ru, encoding='utf-8')

    def test_complete_pair(self):
        self.assertEqual(check_documentation(self.root), 1)

    def test_missing_translation(self):
        self.russian.unlink()
        with self.assertRaisesRegex(ValueError, 'Missing language counterpart'):
            check_documentation(self.root)

    def test_command_change(self):
        self.russian.write_text(self.ru.replace('--limit=4', '--limit=8'), encoding='utf-8')
        with self.assertRaisesRegex(ValueError, 'command/code block differs'):
            check_documentation(self.root)

    def test_missing_section(self):
        self.russian.write_text(self.ru.replace('## Запуск', 'Запуск'), encoding='utf-8')
        with self.assertRaisesRegex(ValueError, 'Section structure differs'):
            check_documentation(self.root)

    def test_broken_link(self):
        self.english.write_text(self.en + '\n[Missing](absent.md)\n', encoding='utf-8')
        with self.assertRaisesRegex(ValueError, 'Broken local link'):
            check_documentation(self.root)

    def test_untranslated_link(self):
        # Existing English target must have a Russian target on a Russian page.
        other = self.root / 'docs/other.md'
        other.write_text(self.en, encoding='utf-8')
        (self.root / 'docs/other.ru.md').write_text(self.ru, encoding='utf-8')
        self.russian.write_text(self.ru + '\n[Other](other.md)\n', encoding='utf-8')
        with self.assertRaisesRegex(ValueError, 'Russian page links to English page'):
            check_documentation(self.root)

    def test_unpaired_change_or_deletion(self):
        with self.assertRaisesRegex(ValueError, 'Update both language files'):
            check_changed_pairs(['docs/guide.md'])
        check_changed_pairs(['docs/guide.md', 'docs/guide.ru.md', 'internal/main.go'])


if __name__ == '__main__':
    unittest.main()
