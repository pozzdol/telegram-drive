# Sharing, links, comments and the trash

A file or folder can be shared with other accounts as viewer, commenter or editor. Sharing a
folder covers everything inside it. Editors may upload, create folders, rename, move (only within
the owner's folder tree), delete and share further; whatever they add stays theirs. Deleted items
go to their owner's trash and are purged, Telegram messages included, after 30 days.

Each item can also have one public link (`/s/<token>`), off by default. Anyone with it can open
the item without an account, as viewer, commenter or editor, optionally behind a password, an
expiry date and a required guest name. Guest uploads count against a per-link cap (2 GB per file
and 20 GB in total by default) and belong to the owner of the folder. The owner always keeps full
access, whatever a guest editor changes, and every access change is logged.

Commenters, editors and the owner can comment on files. When someone else comments, the file's
owner gets a message from the login bot, if they linked Telegram and allowed the bot to write to
them (the login popup asks; accounts linked earlier can link again, or send /start to the bot).

Sharing between separate Telegram Drive servers (`name@other-server`) is being designed in
[docs/federation.md](docs/federation.md); comments on the draft are welcome.

