WAR TABLE
WoW companion overlay for Midnight (patch 12.1.5)

FIRST RUN
1. Double-click WarTable.exe.
   Windows may say "Windows protected your PC" because the app isn't code-signed.
   Click "More info", then "Run anyway".
2. The War Table opens as a small window on the right side of your screen.
3. Click "Install addon" on the setup card. It finds your WoW folder and copies
   the War Table Companion addon into Interface\AddOns.
4. Log into each character (or type /reload). The addon saves your Great Vault,
   Mythic+ runs, keystone and raid lockouts. The app picks it up within seconds.
5. Open Settings (gear icon) and paste your Anthropic API key to use the Ask tab.
   Get one at console.anthropic.com. It's billed to your API account.

PLAYING WITH IT
- In WoW, set Options > Graphics > Display Mode to "Windowed (Fullscreen)" so the
  overlay can sit on top of the game.
- Ctrl+Alt+W hides or shows the War Table.
- The shield icon in your system tray (bottom right, maybe under the ^ arrow) shows
  the reset countdown when you hover it. Click it to show or hide the War Table.
  Right-click for Move to next screen and Quit.
- Each class gets its own look: Warrior (Forge), Shaman (Tidecaller), Fire Mage (Pyre).
  Turn off the sparks and embers in Settings > Overlay > Ambient effects.
- To put it on your second monitor: drag it there, or use Settings > Overlay > Screen.
  It opens wherever you last left it. If that screen is unplugged, it opens on your main one.
- Opacity and always-on-top are in Settings.
- In game, /wt prints your current Vault summary.
- WoW only writes addon data to disk on /reload or logout. If the app looks
  behind, /reload.

UPDATES
- The app checks github.com/ahoule710/war-table for new releases. When one is out,
  "Update ready" appears under the tabs. Click Update now and it restarts itself.

WHERE THINGS LIVE
- Settings, checklist and notes: %APPDATA%\WarTable
- Your API key is encrypted with your Windows login (DPAPI) and never leaves this
  PC except to call api.anthropic.com.
- A spare copy of the addon: %APPDATA%\WarTable\addon

The app opens its window with Google Chrome (or Microsoft Edge if Chrome is missing).
If something goes wrong, %APPDATA%\WarTable\log.txt says what happened.
To uninstall: quit from Settings, delete WarTable.exe, %APPDATA%\WarTable, and
Interface\AddOns\WarTableCompanion.
