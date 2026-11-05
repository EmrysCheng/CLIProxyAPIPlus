import contextlib
import io
import os
from pathlib import Path
import re
import sys
import unittest
from unittest import mock
sys.path.insert(0,str(Path(__file__).parents[1]/'scripts'))
import build_progress

class BuildProgressTests(unittest.TestCase):
    def test_progress_is_opt_in_and_identifiers_are_fixed(self):
        for enabled in ['', '1']:
            out=io.StringIO()
            with mock.patch.dict(os.environ,{'PLUS_SOURCE_BUILD_PROGRESS':enabled}),contextlib.redirect_stdout(out):
                build_progress.step('product_tests')
            self.assertEqual('PLUS_SOURCE_PHASE:product_tests\n' if enabled else '',out.getvalue())
        with self.assertRaises(ValueError):build_progress.step('private-secret')

    def test_go_python_and_ui_stage_names_match(self):
        root=Path(__file__).parents[1]
        go=(root/'internal/webupdate/progress.go').read_text(encoding='utf-8-sig')
        names=re.search(r'var sourceSteps = \[\]string\{(.*?)\}',go,re.S).group(1)
        self.assertEqual(list(build_progress.STEPS),re.findall(r'"([a-z_]+)"',names))
        ui=(root/'patches/cpa-manager-plus/files/apps/web/src/features/system/updateProgressModel.ts').read_text(encoding='utf-8-sig')
        self.assertEqual(list(build_progress.STEPS),re.findall(r"\['([a-z_]+)',",ui.split('] as const;')[0]))
