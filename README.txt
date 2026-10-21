DataRecovery for Windows - SD card / pen drive bata delete bhayeka files recover garne app
=======================================================================================

Install garne
-------------
1. DataRecovery-Setup.exe double-click garnus.
   - Windows SmartScreen le "Windows protected your PC" dekhayo bhane
     "More info" > "Run anyway" thichnus. (App sign gareko chhaina.)
2. Installer ko "Next" / "Install" thichnus. Start Menu ra Desktop ma
   "DataRecovery" shortcut banchha.
   (Computer ma Microsoft WebView2 chhaina bhane installer le aaphai halchha;
   yasko lagi internet chahinchha. Windows 10/11 ma prayah pahile nai hunchha.)

Kasari chalaune
---------------
1. Card laptop ma halnus. Card ma naya kehi copy NAGARNUS.
2. Desktop ko "DataRecovery" kholnus. "Do you want to allow this app to make
   changes" aayo bhane "Yes" thichnus - card sidhai padhna yo chahinchha.
   Card ma kahile pani kei lekhdaina.
3. Mathi dayan tira "English / नेपाली" le bhasha pherna milchha.
4. Card chhannus (jastai E:), Quick scan wa Deep scan chhannus, ani
   "Start scan" thichnus.
   - Quick scan: delete bhayeka files naam ra folder sahit, chhito.
   - Deep scan: naam haraisakeka photos/videos pani (format pachhi pani);
     128 GB card ma 30 minute dekhi 1 ghanta lagna sakchha.
5. Files chhannus. Photo ma click garda thulo preview dekhinchha.
   - Kei nalekheko = thik chha
   - Pahelo = bigreko huna sakchha
   - Rato   = overwrite bhaisakeko
6. Tala save garne folder chhannus ("Browse..."). Yo card bahek arkai thau
   hunu parchha (Desktop pahile nai bhariyeko hunchha). "Recover" thichnus.
7. Sakiye pachhi "Open folder" le files dekhauchha.

Natija kaha aauchha
-------------------
<chhaneko folder>\Recovered_<card ko naam>_<mitti samaya>\<original folder>\<original naam>
  _Found\Photos, _Found\Videos   = deep scan le naam bina bhetayeka
  _LostFolders\folder_<number>   = naam bhetiyo tara folder thaha nabhayeka

Uninstall
---------
Settings > Apps > DataRecovery > Uninstall.

Thaha hunuparne kura
--------------------
- SD card, pen drive, external hard disk ra computer ko afnai drive sabai
  list ma aaphai dekhinchhan. Naya drive jodda aaphai dekhinchha.
- Quick scan (naam ra folder sahit): FAT32, exFAT ra NTFS drive ma chalchha.
- Deep scan: jun sukai drive ma chalchha (APFS, HFS+, format nabhaeko/bigreko
  drive pani) - photos/videos lai content bata khojchha.
- Laptop ko afnai SSD ma TRIM le delete bhayeka files turuntai mettachha, ra
  Mac ko afnai drive encrypted hunchha - tesaile yinbata dherai kam files
  aaunchhan. Card, pen drive ra hard disk ma yo samasya hudaina.
- FAT32 ma chhoto naam bhaeko folder ko pahilo akshar "_" dekhina sakchha
  (jastai "_MAGES"); yo FAT32 le nai mettachha.
- Card ma naya files lekhiyeko thau ma purana files overwrite hunchhan.
  Jati chhito recover garyo, utti dherai files aaunchhan.

Developer: installer feri banaune
---------------------------------
  scripts/build-installers.sh   (Mac ma; Go, Wails CLI ra NSIS chahinchha)

----------------------------------------
Built by Arun Pandey - Arun AI Studio
Website: https://arun-pandey.com.np/
© 2026 Arun Pandey. All rights reserved.
