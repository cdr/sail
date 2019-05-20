+++
type="docs"
title="Project-defined Extensions"
browser_title="Sail - Docs - Project-defined Extensions"
section_order=2
+++

Installing VS Code extensions through your Sail Dockerfile is dead-simple.

In your Dockerfile, call `installext <extension ID>`.

For example:

```Dockerfile
FROM ubuntu-dev
RUN installext vscodevim.vim
```


**`ubuntu-dev` or `ubuntu-dev-*` must be your image's base for `installext` to be available.**

---

_Tip: Find an extension's ID at it's marketplace page:_

![Extension ID in VS Code](/extension-id.png)


## Under The Hood

`code-server` is started with two extension directories:

- `~/.vscode/extensions` contains extensions for the specific environment.
- `~/.vscode/host-extensions` is bind-mounted in from `~/.vscode/extensions` on
the host.

This ensures that

1. Projects can specify their extensions.
1. Users continue using the extensions that they installed through native
VS Code.

