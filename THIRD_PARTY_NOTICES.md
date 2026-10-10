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

The reviewed pro-bing module graph uses the following modules and versions.
The root module pins this graph and its checksums; `go mod verify` passed. The
resolved licenses are permissive.

| Module | Version | License | Copyright / attribution |
| --- | --- | --- | --- |
| `github.com/prometheus-community/pro-bing` | `v0.9.2-0.20261008151844-bcbba8f702ab` | MIT; source headers Apache-2.0 | The Prometheus Authors; Cameron Sparr and contributors |
| `github.com/google/uuid` | `v1.6.0` | BSD-3-Clause | Google Inc. (2009, 2014) |
| `golang.org/x/crypto` | `v0.53.0` | BSD-3-Clause | The Go Authors (2009) |
| `golang.org/x/net` | `v0.56.0` | BSD-3-Clause | The Go Authors (2009) |
| `golang.org/x/sync` | `v0.21.0` | BSD-3-Clause | The Go Authors (2009) |
| `golang.org/x/sys` | `v0.46.0` | BSD-3-Clause | The Go Authors (2009) |
| `golang.org/x/term` | `v0.44.0` | BSD-3-Clause | The Go Authors (2009) |
| `golang.org/x/text` | `v0.38.0` | BSD-3-Clause | The Go Authors (2009) |

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

The golang.org/x modules above carry identical BSD-3-Clause license text. This
text follows.

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
