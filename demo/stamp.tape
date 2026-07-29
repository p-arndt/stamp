# stamp — README demo tape.
#
# Recorded by `just demo`, which builds stamp and hands this tape a throwaway
# repository: a bare remote plus a work tree with a VERSION file and a
# package.json, created in $TMPDIR. Nothing on screen touches a real project, so
# anyone can re-record the GIF without exposing anything of their own.
#
# The setup is hidden in the recording but not from you: it is demo/setup.sh,
# whose absolute path `just demo` passes in as $DEMO_SETUP.

Output assets/demo.gif

Require stamp
Require git

Set Shell "bash"
Set FontSize 15
Set Width 1200
Set Height 900
Set Padding 26
Set Margin 18
Set MarginFill "#1a1420"
Set BorderRadius 10
Set Theme "Catppuccin Mocha"
Set TypingSpeed 55ms
Set Framerate 24

# ---- a throwaway repository: bare remote + work tree at 0.4.0 ----

Hide
Type "source $DEMO_SETUP" Enter
Sleep 2s
Type "clear" Enter
Show

# ---- the release ----

Type "stamp release minor" Enter
Wait+Screen /Proceed/
Sleep 3s

Type "y" Enter
# A plain Sleep, not Wait+Screen: the push writes progress lines of its own, and
# waiting on a pattern races with them.
Sleep 6s

# A last beat so the GIF rests on the finished release (and the screenshot flushes).
Sleep 2s
