"""Serialize builds/staging/source promotion in a single local workspace."""
from contextlib import contextmanager
import os
@contextmanager
def file_lock(path):
    path.parent.mkdir(parents=True,exist_ok=True)
    with path.open('a+b') as file:
        file.seek(0)
        try:
            if os.name=='nt':
                import msvcrt
                msvcrt.locking(file.fileno(),msvcrt.LK_NBLCK,1)
            else:
                import fcntl
                fcntl.flock(file.fileno(),fcntl.LOCK_EX|fcntl.LOCK_NB)
        except OSError as error:raise ValueError('Workspace is busy building/staging/promoting; retry after the current operation exits') from error
        try:yield
        finally:
            file.seek(0)
            if os.name=='nt':msvcrt.locking(file.fileno(),msvcrt.LK_UNLCK,1)
            else:fcntl.flock(file.fileno(),fcntl.LOCK_UN)
