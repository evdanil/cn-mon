# Third-party notices

This file records third-party code and licenses used by cn-mon. The root
[LICENSE](LICENSE) applies to first-party code. It is Apache-2.0 and does not
replace third-party licenses.

## pro-bing

The native ICMP core is derived from
[`github.com/prometheus-community/pro-bing`](https://github.com/prometheus-community/pro-bing),
upstream commit `bcbba8f702ab2b7a4a62697f10b27491d50b520b`, resolved by Go as
`v0.9.2-0.20261008151844-bcbba8f702ab`. The local module is under
`third_party/pro-bing`; its copied root `LICENSE` is retained. The upstream root
license is MIT, with copyright notices for The Prometheus Authors and Cameron
Sparr and contributors. Retained Go source files also carry Apache-2.0 headers;
the Apache-2.0 text is available in the repository root `LICENSE`.

The local adaptation retains these upstream files from the pinned revision:
`LICENSE`, `go.mod`, `go.sum`, `logger.go`, `packetconn.go`, `ping.go`,
`ping_test.go`, `utils_linux.go`, `utils_other.go`, `utils_windows.go`, and
`utils_windows_test.go`. No upstream HTTP handlers or examples were copied.
Added files are `icmp_quote.go`, `icmp_error_test.go`, `icmp_matrix_test.go`,
`ping_native_test.go`, and `utils_linux_test.go`; the added Go code is covered
by the root Apache-2.0 license.

The `ping.go` adaptation adds a structured ICMP error callback and a bounded
two-successful-send ledger for correlating quoted requests independently from
echo replies. It parses only received packet bytes, uses full-size receive
buffers, propagates send failures, suppresses routine read-timeout callbacks,
removes unbounded permission/resource retries, and treats filter installation
failure as an error. Parent cancellation and host timeout close the run-owned
socket before worker joins, while retaining independent worker failures.
`icmp_quote.go` validates quoted IPv4/IPv6 errors and extension headers,
respects declared packet lengths, checks available request tokens, and
deduplicates target/intermediate attribution with scope-aware addresses.
Linux filtering keeps the echo identifier fast path while admitting supported
ICMP errors. Tests include offline upstream assertions, quoted-packet cases,
Linux BPF checks, and blocked-I/O lifecycle cases.

The native Linux and Windows runtime gates, including raw ICMP permission
outcomes, passed in [CI run
38037349729](https://github.com/evdanil/gotest/actions/runs/38037349729):
[Linux native job](https://github.com/evdanil/gotest/actions/runs/38037349729/job/114170451322)
and [Windows native job](https://github.com/evdanil/gotest/actions/runs/38037349729/job/114170451856).
The run's ordinary Linux and Windows validation jobs passed as well; the job
artifacts contain the native phase logs and platform details. The Digineo
`go-ping` project was consulted as a parsing reference only; it is not a
runtime dependency and no code from it is copied.

## cn-tool behavior provenance

The monitor behavior and compatibility requirements are characterized against
[`cn-tool` v0.8.0, commit `2e2f3c40ef6718486f1ca4477f9145029ec495fa`](https://github.com/evdanil/cn-tool/tree/2e2f3c40ef6718486f1ca4477f9145029ec495fa).
That source is licensed under Apache-2.0. cn-mon is a native Go
reimplementation; no Python implementation code or Python runtime is included.
The root [LICENSE](LICENSE) contains the Apache-2.0 text.

## Go module dependency inventory

The root `go.mod` selects the application dependencies and Go resolves the
complete graph from their module metadata. `go mod verify` passed for modules
downloaded into the module cache; root `go.sum` contains checksums required by
the resolved build and test graph, not a separate line for each graph-only
module.
`go list -deps` on both Linux/amd64 and Windows/amd64 confirms these modules
are imported by the application packages:

| Module | Version | License | Copyright / attribution |
| --- | --- | --- | --- |
| `github.com/prometheus-community/pro-bing` | `v0.9.2-0.20261008151844-bcbba8f702ab` | MIT; retained source headers Apache-2.0 | The Prometheus Authors; Cameron Sparr and contributors |
| `github.com/google/uuid` | `v1.6.0` | BSD-3-Clause | Google Inc. (2009, 2014) |
| `github.com/rivo/uniseg` | `v0.4.7` | MIT; generated Unicode property tables use Unicode-DFS-2016 | Oliver Kuederle (2019); Unicode, Inc. (Unicode 15.0 data) |
| `github.com/ncruces/go-sqlite3` | `v0.35.6` | MIT | Nuno Cruces (2023, 2026) |
| `github.com/ncruces/go-sqlite3-wasm/v6` | `v6.3.35304` | MIT No Attribution for remaining wrapper code; generated Go retains upstream licenses, including SQLite public domain | Nuno Cruces (2026); SQLite authors |
| `github.com/ncruces/julianday` | `v1.0.0` | MIT | Nuno Cruces (2022) |
| `golang.org/x/net` | `v0.56.0` | BSD-3-Clause | The Go Authors (2009) |
| `golang.org/x/sync` | `v0.23.0` | BSD-3-Clause | The Go Authors (2009) |
| `golang.org/x/sys` | `v0.48.0` | BSD-3-Clause | The Go Authors (2009) |
| `golang.org/x/term` | `v0.44.0` | BSD-3-Clause | The Go Authors (2009) |

The full module graph also contains non-imported dependencies used by upstream
build tooling, tests, and module metadata. These are inherited through the
resolved module graph but are not compiled into the application. For example,
the M3 history
driver's production import closure on both supported operating systems contains
`go-sqlite3`, generated Go translation of SQLite, `julianday`, and `x/sys`; it
does not link a system SQLite library or require CGO. The translation was
produced with `wasm2go`; that generator is build-time tooling, not a runtime
dependency. The generated compatibility shims, including the TLSF allocator,
retain wasm2go's MIT notice. The bundled `stb_sprintf.h` source offers MIT and
public-domain alternatives; this distribution retains its MIT notice
(Sean Barrett, 2017). SQLite code remains public domain. The history interop gate
reported SQLite 3.53.4 through the Go driver and source Python SQLite 3.53.1
on Linux.
The native Windows job records its own versions and results in CI artifacts and
annotations.

The resolved root module graph additionally includes these transitive
metadata/tooling modules, which are not in the Linux/Windows application import
closure. These versions come from the resolved graph and are recorded here for
inventory; they are inherited through dependency metadata, not all direct
requirements or individually listed in root `go.sum`:

| Module | Version | License | Copyright / attribution |
| --- | --- | --- | --- |
| `github.com/dchest/siphash` | `v1.2.3` | CC0-1.0 | No attribution required by license |
| `github.com/ncruces/sort` | `v1.0.2` | MIT No Attribution | Nuno Cruces (2026) |
| `github.com/ncruces/wasm2go` | `v0.4.15` | MIT | Nuno Cruces |
| `github.com/ncruces/wbt` | `v1.0.0` | MIT | Nuno Cruces |
| `github.com/psanford/httpreadat` | `v0.1.0` | MIT | Upstream authors |
| `golang.org/x/crypto` | `v0.57.0` | BSD-3-Clause | The Go Authors (2009) |
| `golang.org/x/text` | `v0.42.0` | BSD-3-Clause | The Go Authors (2009) |
| `golang.org/x/tools` | `v0.50.0` | BSD-3-Clause | The Go Authors (2009) |
| `lukechampine.com/adiantum` | `v1.1.1` | MIT | Luke Champine (2019) |

The Google UUID license text follows.

```text
Copyright (c) 2009,2014 Google Inc. All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google Inc. nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

The BSD-3-Clause text below covers the listed golang.org/x modules and the Go
runtime and standard library distributed in the cn-mon binaries. The same Go
license text applies to all of them; it is included once here.

```text
Copyright 2009 The Go Authors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google LLC nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

The pro-bing MIT license text follows.

```text
The MIT License (MIT)

Copyright 2022 The Prometheus Authors
Copyright 2016 Cameron Sparr and contributors.

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

The SQLite driver and `julianday` use the MIT License; the driver and generated
Go compatibility sources carry these upstream notices:

```text
MIT License

Copyright (c) 2023 Nuno Cruces
Copyright (c) 2022 Nuno Cruces
Copyright (c) 2026 Nuno Cruces
Copyright (c) 2017 Sean Barrett

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

The untranslated wrapper/build material in `go-sqlite3-wasm` uses MIT No
Attribution. Generated Go translation code retains the upstream licenses
described above. The MIT-0 license text follows.

```text
MIT No Attribution License

Copyright (c) 2026 Nuno Cruces

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

SQLite code translated into generated Go remains public domain. The SQLite
distribution's license notice follows.

```text
SQLite Is Public Domain

All of the code and documentation in SQLite has been dedicated to the public
domain by the authors. All code authors, and representatives of the companies
they work for, have signed affidavits dedicating their contributions to the
public domain and originals of those signed affidavits are stored in a firesafe
at the main offices of Hwaci. Anyone is free to copy, modify, publish, use,
compile, sell, or distribute the original SQLite code, either in source code
form or as a compiled binary, for any purpose, commercial or non-commercial,
and by any means.

The previous paragraph applies to the deliverable code and documentation in
SQLite - those parts of the SQLite library that you actually bundle and ship
with a larger application. Some scripts used as part of the build process (for
example the "configure" scripts generated by autoconf) might fall under other
open-source licenses. Nothing from these build scripts ever reaches the final
deliverable SQLite library, however, and the licenses associated with those
scripts should not be a factor in assessing your rights to copy and use the
SQLite library.

All of the deliverable code in SQLite has been written from scratch. No code
has been taken from other projects or from the open internet. Every line of
code can be traced back to its original author, and all of those authors have
public domain dedications on file.
```

The github.com/rivo/uniseg v0.4.7 package is distributed under the MIT License
and includes generated grapheme, line-break, and sentence-break property tables
derived from Unicode 15.0. Its upstream source files state that the Unicode
data was retrieved on September 5, 2023. The upstream LICENSE.txt text follows.
The generated data also carries the Unicode License Agreement:

```text
MIT License

Copyright (c) 2019 Oliver Kuederle

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```

```text
UNICODE, INC. LICENSE AGREEMENT - DATA FILES AND SOFTWARE

See Terms of Use <https://www.unicode.org/copyright.html>
for definitions of Unicode Inc.’s Data Files and Software.

NOTICE TO USER: Carefully read the following legal agreement.
BY DOWNLOADING, INSTALLING, COPYING OR OTHERWISE USING UNICODE INC.'S
DATA FILES ("DATA FILES"), AND/OR SOFTWARE ("SOFTWARE"),
YOU UNEQUIVOCALLY ACCEPT, AND AGREE TO BE BOUND BY, ALL OF THE
TERMS AND CONDITIONS OF THIS AGREEMENT.
IF YOU DO NOT AGREE, DO NOT DOWNLOAD, INSTALL, COPY, DISTRIBUTE OR USE
THE DATA FILES OR SOFTWARE.

COPYRIGHT AND PERMISSION NOTICE

Copyright © 1991-2022 Unicode, Inc. All rights reserved.
Distributed under the Terms of Use in https://www.unicode.org/copyright.html.

Permission is hereby granted, free of charge, to any person obtaining
a copy of the Unicode data files and any associated documentation
(the "Data Files") or Unicode software and any associated documentation
(the "Software") to deal in the Data Files or Software
without restriction, including without limitation the rights to use,
copy, modify, merge, publish, distribute, and/or sell copies of
the Data Files or Software, and to permit persons to whom the Data Files
or Software are furnished to do so, provided that either
(a) this copyright and permission notice appear with all copies
of the Data Files or Software, or
(b) this copyright and permission notice appear in associated
Documentation.

THE DATA FILES AND SOFTWARE ARE PROVIDED "AS IS", WITHOUT WARRANTY OF
ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE
WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND
NONINFRINGEMENT OF THIRD PARTY RIGHTS.
IN NO EVENT SHALL THE COPYRIGHT HOLDER OR HOLDERS INCLUDED IN THIS
NOTICE BE LIABLE FOR ANY CLAIM, OR ANY SPECIAL INDIRECT OR CONSEQUENTIAL
DAMAGES, OR ANY DAMAGES WHATSOEVER RESULTING FROM LOSS OF USE,
DATA OR PROFITS, WHETHER IN AN ACTION OF CONTRACT, NEGLIGENCE OR OTHER
TORTIOUS ACTION, ARISING OUT OF OR IN CONNECTION WITH THE USE OR
PERFORMANCE OF THE DATA FILES OR SOFTWARE.

Except as contained in this notice, the name of a copyright holder
shall not be used in advertising or otherwise to promote the sale,
use or other dealings in these Data Files or Software without prior
written authorization of the copyright holder.
```
