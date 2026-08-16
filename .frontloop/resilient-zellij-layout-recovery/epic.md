---
title: Resilient Zellij layout recovery
status: active
---

## Goal

Make `redeem resume --all` safely reconstruct the intended set of Zellij-backed Kitty windows, Niri workspaces, and terminal column order after either a same-boot Niri failure or a machine reboot, without duplicate attachment, false session creation, or resurrection of unrelated historical sessions.
