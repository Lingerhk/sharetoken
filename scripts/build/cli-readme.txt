ShareToken command-line client @VERSION@@LABEL@

Buy and share open-weight model inference on the ShareToken network, with no
desktop app and no window system.

  @EXE@ login -email you@example.com        sign in with the code mailed to you and bind
                                            this device; a new address gets an account
  @EXE@ up                                  run; prints the local OpenAI endpoint
  @EXE@ status                              from another terminal, while it runs
  @EXE@ -h                                  all commands

Sharing a local model:

  @EXE@ share detect
  @EXE@ share add <model>
  @EXE@ share on

`@EXE@ version` shows which network and state directory this program uses.
