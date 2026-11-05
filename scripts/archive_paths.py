"""Reject archive names that are unsafe on either POSIX or Windows."""
from pathlib import PurePosixPath
import re
DEVICE=re.compile(r'^(CON|PRN|AUX|NUL|COM[1-9]|LPT[1-9])(?:\.|$)',re.IGNORECASE)
def validate(name,directory=False):
    if not isinstance(name,str) or not name or '\\' in name or ':' in name or any(ord(c)<32 for c in name):raise ValueError('Unsafe archive entry')
    raw=name[:-1] if directory and name.endswith('/') else name
    parts=raw.split('/')
    if PurePosixPath(raw).is_absolute() or any(not p or p in ['.','..'] or p.endswith((' ','.')) or p.casefold()=='.git' or DEVICE.match(p) for p in parts):raise ValueError('Unsafe archive entry')
    return parts
