A copy of [github.com/ledongthuc/pdf](https://github.com/ledongthuc/pdf) (BSD-style
license, see LICENSE), with one change in `page.go`: `Page.Content()` follows Form
XObjects (the `Do` operator). The House Clerk's e-filed PTR PDFs keep all their text
inside a Form XObject, so the upstream package returns no text for them.
