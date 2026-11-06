"""Native Windows directory-reader contention must not abort a verified restore."""
from contextlib import closing
import ctypes
from ctypes import wintypes
import os
from pathlib import Path
import sqlite3
import sys
import tempfile
import threading
import unittest
from unittest import mock

sys.path.insert(0, str(Path(__file__).parents[1] / 'scripts'))
import data


@unittest.skipUnless(os.name == 'nt', 'requires native Windows directory-sharing semantics')
class WindowsRestoreSharingTests(unittest.TestCase):
    def test_restore_waits_for_short_lived_restored_file_reader(self):
        kernel = ctypes.WinDLL('kernel32', use_last_error=True)
        kernel.CreateFileW.argtypes = [wintypes.LPCWSTR, wintypes.DWORD, wintypes.DWORD, ctypes.c_void_p, wintypes.DWORD, wintypes.DWORD, wintypes.HANDLE]
        kernel.CreateFileW.restype = wintypes.HANDLE
        kernel.CloseHandle.argtypes = [wintypes.HANDLE]
        kernel.CloseHandle.restype = wintypes.BOOL
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            original = root / 'original'
            (original / 'auths').mkdir(parents=True)
            (original / 'data.key').write_text('fixture-key', encoding='utf-8')
            (original / 'auths/account.json').write_text('{"token":"fixture"}', encoding='utf-8')
            with closing(sqlite3.connect(original / 'usage.sqlite')) as database:
                database.execute('create table fixture(value text)')
                database.execute("insert into fixture values('retained')")
                database.commit()
            config = root / 'config.yaml'
            config.write_text('host: 127.0.0.1\n', encoding='utf-8')
            archive = root / 'backup.zip'
            data.snapshot(config, original, archive, {'auth_dir': str(original / 'auths'), 'tls_enabled': False})
            original_rename = Path.rename
            handles = []
            timers = []
            attempts = []
            def contended_rename(source, destination):
                if source.name == 'data' and source.parent.name.startswith('.restore-') and source.parent.parent == root:
                    attempts.append(source)
                    if not handles:
                        # A legitimate reader of a restored file deliberately omits FILE_SHARE_DELETE.
                        handle = kernel.CreateFileW(str(source / 'data.key'), 0x80000000, 0x3, None, 3, 0x80, None)
                        if handle == wintypes.HANDLE(-1).value:
                            raise ctypes.WinError(ctypes.get_last_error())
                        handles.append(handle)
                        timer = threading.Timer(0.2, lambda: kernel.CloseHandle(handle))
                        timers.append(timer)
                        timer.start()
                return original_rename(source, destination)
            try:
                with mock.patch.object(Path, 'rename', new=contended_rename), mock.patch.object(data, 'run_binary', return_value=''):
                    data.restore(archive, root / 'restored', root / 'restored.yaml', Path('unused'))
            finally:
                for timer in timers:
                    timer.join()
            self.assertGreater(len(attempts), 1, 'the native restored-file reader must actually contend with rename')
            self.assertEqual((root / 'restored/data.key').read_text(), 'fixture-key')
            with closing(sqlite3.connect(root / 'restored/usage.sqlite')) as database:
                self.assertEqual(database.execute('select value from fixture').fetchone()[0], 'retained')


    def test_permanent_denial_is_bounded_and_does_not_weaken_permissions(self):
        with tempfile.TemporaryDirectory() as temporary:
            source = Path(temporary) / 'stage'
            source.mkdir()
            destination = Path(temporary) / 'data'
            denied = PermissionError(13, 'synthetic permanent denial')
            denied.winerror = 5
            with mock.patch.object(Path, 'rename', side_effect=denied) as rename, mock.patch.object(data.time, 'sleep') as sleep, mock.patch.object(data.private, 'protect') as protect, mock.patch.object(data.os, 'chmod') as chmod:
                with self.assertRaises(PermissionError) as raised:
                    data._publish_restore_directory(source, destination)
            self.assertIs(raised.exception, denied)
            self.assertEqual(rename.call_count, 6)
            self.assertEqual(sleep.call_count, 5)
            protect.assert_not_called()
            chmod.assert_not_called()
            self.assertTrue(source.is_dir())
            self.assertFalse(destination.exists())

    def test_staging_identity_is_rechecked_after_contention(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            source, destination = root / 'stage', root / 'data'
            source.mkdir()
            denied = PermissionError(13, 'synthetic contention')
            denied.winerror = 5
            original_rename = Path.rename
            def replace_staging(_delay):
                original_rename(source, root / 'original-stage')
                source.mkdir()
            with mock.patch.object(Path, 'rename', side_effect=denied), mock.patch.object(data.time, 'sleep', side_effect=replace_staging):
                with self.assertRaisesRegex(ValueError, 'staging directory changed'):
                    data._publish_restore_directory(source, destination)
            self.assertFalse(destination.exists())
            self.assertTrue((root / 'original-stage').is_dir())


class RestoreDirectoryPublicationTests(unittest.TestCase):
    def test_destination_is_never_overwritten(self):
        with tempfile.TemporaryDirectory() as temporary:
            source, destination = Path(temporary) / 'stage', Path(temporary) / 'data'
            source.mkdir()
            destination.mkdir()
            (destination / 'retained').write_text('existing', encoding='utf-8')
            with mock.patch.object(Path, 'rename') as rename:
                with self.assertRaises(FileExistsError):
                    data._publish_restore_directory(source, destination)
            rename.assert_not_called()
            self.assertEqual((destination / 'retained').read_text(), 'existing')

    def test_unrelated_error_is_not_retried(self):
        with tempfile.TemporaryDirectory() as temporary:
            source = Path(temporary) / 'stage'
            source.mkdir()
            error = OSError(28, 'fixture disk full')
            with mock.patch.object(Path, 'rename', side_effect=error) as rename, mock.patch.object(data.time, 'sleep') as sleep:
                with self.assertRaises(OSError) as raised:
                    data._publish_restore_directory(source, Path(temporary) / 'data')
            self.assertIs(raised.exception, error)
            self.assertEqual(rename.call_count, 1)
            sleep.assert_not_called()
