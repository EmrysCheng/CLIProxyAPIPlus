"""Regression runner budgets must cover instrumented SQLite fixtures, not disable them."""
from pathlib import Path
import sys
import tempfile
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).parents[1] / 'scripts'))
import regression


class RegressionBudgetTests(unittest.TestCase):
    def commands(self, *args):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            with mock.patch.object(regression.project, 'prepare'), mock.patch.object(regression.project, 'ROOT', root), mock.patch.object(regression.project, 'BUILD', root / '.build'), mock.patch.object(regression.subprocess, 'run') as run, mock.patch.object(sys, 'argv', ['regression.py', *args]):
                regression.main()
            return [call.args[0] for call in run.call_args_list]

    def test_race_sqlite_suite_has_explicit_instrumentation_budget(self):
        commands = self.commands('--race')
        suite = commands[0]
        self.assertIn('-timeout', suite)
        self.assertEqual(suite[suite.index('-timeout') + 1], '30m')
        for flag in ['-race', '-count=1', '-p=1', '-parallel=1']:
            self.assertIn(flag, suite)
        self.assertIn('github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite', suite)
        self.assertEqual(sum(command[1] == 'build' for command in commands), 2)

    def test_full_upstream_scope_is_retained_and_bounded(self):
        suite = self.commands('--all-upstream')[0]
        self.assertIn('-timeout', suite)
        self.assertEqual(suite[suite.index('-timeout') + 1], '20m')
        self.assertNotIn('-race', suite)
        self.assertIn('github.com/seakee/cpa-manager-plus/apps/manager-server/...', suite)
        self.assertIn('github.com/router-for-me/CLIProxyAPI/v8/...', suite)
        self.assertNotIn('-run', suite)
