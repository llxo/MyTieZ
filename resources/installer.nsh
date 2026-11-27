LangString MYTIEZ_DELETE_DATA_PROMPT ${LANG_SIMPCHINESE} "是否同时删除 MyTieZ 的本地数据（剪贴板历史、图片与设置）？$\n$\n保留数据可方便日后重新安装时恢复。"
LangString MYTIEZ_DELETE_DATA_PROMPT ${LANG_TRADCHINESE} "是否同時刪除 MyTieZ 的本機資料（剪貼簿歷史、圖片與設定）？$\n$\n保留資料可方便日後重新安裝時恢復。"
LangString MYTIEZ_DELETE_DATA_PROMPT ${LANG_ENGLISH} "Also remove MyTieZ local data (clipboard history, images, and settings)?$\n$\nKeeping your data allows restoring it if you reinstall later."

!macro customUnInstall
  DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "io.github.llxo.mytiez"
  DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run" "io.github.llxo.mytiez"
  IfSilent mytiez_skip_delete_data
  IfFileExists "$LOCALAPPDATA\MyTieZ\*.*" 0 mytiez_skip_delete_data
    MessageBox MB_YESNO|MB_ICONQUESTION|MB_DEFBUTTON2 "$(MYTIEZ_DELETE_DATA_PROMPT)" /SD IDNO IDNO mytiez_skip_delete_data
    RMDir /r "$LOCALAPPDATA\MyTieZ"
  mytiez_skip_delete_data:
!macroend