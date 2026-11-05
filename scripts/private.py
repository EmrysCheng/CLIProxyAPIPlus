"""Private maintenance artifacts: POSIX modes or a protected Windows DACL.

Do not rely on Python-version-dependent os.mkdir(0700) semantics on Windows.
Call protect immediately after creation, before writing secret data.
"""
import os

def protect(path,directory=False):
    if os.name!='nt':
        os.chmod(path,0o700 if directory else 0o600)
        return
    import ctypes
    from ctypes import wintypes as w
    adv=ctypes.WinDLL('advapi32',use_last_error=True)
    kernel=ctypes.WinDLL('kernel32',use_last_error=True)
    kernel.GetCurrentProcess.restype=w.HANDLE
    kernel.CloseHandle.argtypes=[w.HANDLE]
    kernel.LocalFree.argtypes=[ctypes.c_void_p]
    adv.OpenProcessToken.argtypes=[w.HANDLE,w.DWORD,ctypes.POINTER(w.HANDLE)]
    adv.GetTokenInformation.argtypes=[w.HANDLE,w.DWORD,ctypes.c_void_p,w.DWORD,ctypes.POINTER(w.DWORD)]
    adv.ConvertSidToStringSidW.argtypes=[ctypes.c_void_p,ctypes.POINTER(w.LPWSTR)]
    adv.ConvertStringSecurityDescriptorToSecurityDescriptorW.argtypes=[w.LPCWSTR,w.DWORD,ctypes.POINTER(ctypes.c_void_p),ctypes.POINTER(w.DWORD)]
    adv.GetSecurityDescriptorDacl.argtypes=[ctypes.c_void_p,ctypes.POINTER(w.BOOL),ctypes.POINTER(ctypes.c_void_p),ctypes.POINTER(w.BOOL)]
    adv.SetNamedSecurityInfoW.argtypes=[w.LPWSTR,w.DWORD,w.DWORD,ctypes.c_void_p,ctypes.c_void_p,ctypes.c_void_p,ctypes.c_void_p]
    adv.SetNamedSecurityInfoW.restype=w.DWORD
    token=w.HANDLE()
    if not adv.OpenProcessToken(kernel.GetCurrentProcess(),8,ctypes.byref(token)):raise ctypes.WinError(ctypes.get_last_error())
    sid_text=w.LPWSTR();descriptor=ctypes.c_void_p()
    try:
        size=w.DWORD();adv.GetTokenInformation(token,1,None,0,ctypes.byref(size))
        buffer=ctypes.create_string_buffer(size.value)
        if not adv.GetTokenInformation(token,1,buffer,size.value,ctypes.byref(size)):raise ctypes.WinError(ctypes.get_last_error())
        # TOKEN_USER starts with SID_AND_ATTRIBUTES; its first field is PSID.
        sid=ctypes.cast(buffer,ctypes.POINTER(ctypes.c_void_p))[0]
        if not adv.ConvertSidToStringSidW(sid,ctypes.byref(sid_text)):raise ctypes.WinError(ctypes.get_last_error())
        flags='OICI' if directory else ''
        sddl='D:P(A;'+flags+';FA;;;'+sid_text.value+')(A;'+flags+';FA;;;SY)(A;'+flags+';FA;;;BA)'
        if not adv.ConvertStringSecurityDescriptorToSecurityDescriptorW(sddl,1,ctypes.byref(descriptor),None):raise ctypes.WinError(ctypes.get_last_error())
        present=w.BOOL();defaulted=w.BOOL();acl=ctypes.c_void_p()
        if not adv.GetSecurityDescriptorDacl(descriptor,ctypes.byref(present),ctypes.byref(acl),ctypes.byref(defaulted)) or not present.value:raise OSError('Private DACL missing')
        result=adv.SetNamedSecurityInfoW(str(path),1,0x80000004,None,None,acl,None)
        if result:raise ctypes.WinError(result)
    finally:
        if descriptor:kernel.LocalFree(descriptor)
        if sid_text:kernel.LocalFree(ctypes.cast(sid_text,ctypes.c_void_p))
        kernel.CloseHandle(token)
