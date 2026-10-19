# Upstream decisions

Журнал решений по коммитам `pdfcpu/pdfcpu`, которые мы не берём. Нужен, чтобы через время не разбирать их заново.

Принцип: обработанный файл лучше ошибки. Из upstream берём только то, что не может отклонить PDF, который мы сейчас успешно обрабатываем. Всё, что добавляет проверку или лимит, берём только после разбора, какие входные файлы оно отвергает.

Как обновлять:

1. `git fetch upstream && git log master..upstream/master` показывает только новые коммиты (всё прежнее учтено merge-коммитами `-s ours`, см. ниже).
2. Новые коммиты сверяются с правилами и таблицей ниже; решение дописывается сюда.
3. Нужное берётся через `git cherry-pick -x`.
4. Заканчивается обновление merge-коммитом `git merge -s ours upstream/master`: он отмечает весь upstream учтённым, не меняя содержимое. В теле коммита перечисляются взятые и отклонённые коммиты.

## Собственные изменения производительности

Документы с сотнями тысяч объектов (тайловые сканы, где картинок сотни тысяч) упирались в квадратичные места и удержание памяти. При обновлении из upstream эти места не откатывать:

| Где | Что |
|---|---|
| `model/fingerprint.go`, `optimize.go` | Поиск дубликатов картинок по отпечатку содержимого: сравниваются только кандидаты с тем же отпечатком, а не вся корзина по хешу потока. |
| `model/equal.go` | `null` равен только `null` (раньше `null` был равен чему угодно, кроме `null`). |
| `types/dict.go` | `Dict.Clone` линейный; `Find` при промахе декодирует только имена с `#`. |
| `optimize.go` | Посещённые ресурсы хранятся в map, а не ищутся в срезе. |
| `read.go` | Из буфера чтения копируется только заголовок объекта, а не весь буфер. |
| `image_stream_backing.go`, `types/stream_source.go` | `SpillImageStreams` пишет все потоки картинок в один файл, каждый поток читает свой участок. |
| `filter/flateDecode.go`, `model/image_png_file.go` | zlib writer и reader (вместе с `bufio.Reader`) берутся из пулов `AcquireZlibWriter` / `AcquireZlibReader`, а не создаются на каждый поток. |
| `read.go` | `positionedReader` для объектов и потоков берётся из пула; `growBufBy` растит буфер через `slices.Grow`. |

## Правила по умолчанию

| Область | Решение | Причина |
|---|---|---|
| `pkg/pdfcpu/validate/` (media, 3D, RichMedia, Sound, Movie, Rendition, action, name tree, внешние ссылки, embedded files) | не берём | Строже проверка, сжатию эти проверки не нужны, но ошибка валидации превращается у нас в `ErrPDFInvalid` и отклоняет рабочий PDF. |
| Отчёты об активном содержимом, внешних ссылках, пароли в URL | не берём | Относятся к CLI-валидации ссылок, в сервисе не используются. |
| Защита от паник и переполнений (xref, декодирование, CMYK, палитры, TIFF, BER) | берём | Заменяют панику или мусор на ошибку по одной картинке, файл остаётся (`131c203` в backend). |
| Исправления разбора, терпимость к повреждениям (`#NNN`) | берём | Расширяют круг читаемых файлов. |
| Новые лимиты и проверки | только после разбора | См. решение по коммиту. |

## Коммиты

Baseline: upstream `d4effbd9` (28 September 2026). Reviewed through `b41a41d5` (11 October 2026); decisions up to `2f7a89a0` are listed first.

| Коммит | Решение | Причина |
|---|---|---|
| `eff82e80` prevent panics when rendering grayscale and indexed images | берём | Паника и мусор → ошибка по невалидным bpc/длине; допустимые значения не затронуты. |
| `1fb69ee1` update hhrutter/tiff to v1.0.7 | берём | Зависимость, поведение компрессора не меняется. |
| `b3d78c01` bound JSON input size | берём | Только CLI/конфигурация. |
| `ca15b27b` harden xref parsing and repair | берём | Nil-записи и нулевые длины → ошибка вместо паники; новый лимит `Size` 1…10 млн объектов, за пределами → repair. |
| `228469a5` enforce image decode limits and prevent CMYK extraction panics | берём | Лимит декодирования потока 512 МБ и CMYK только 8 бит: патологические случаи, раньше паника или мусор; картинка остаётся как есть. |
| `64473c7a` bound multi-page TIFF traversal | берём | Импорт TIFF, сжатие не затрагивает. |
| `8a5bf1dd` bound CMS BER parsing and DER conversion | берём | Подписи, сжатие не затрагивает. |
| `a251c310` fix #1493 | берём | Терпимее разбор дат. |
| `b895d85b` fix #612 | берём | В Relaxed запись xref «in-use, offset 0» пропускается, а не ломает чтение. |
| `98098e11` fix Indexed image Decode mapping and palette clamping | берём | Индекс палитры зажимается по HiVal, как и наше исправление в backend. |
| `7843f3bf`, `18c27900`, `36c55970`, `1a61f880`, `e3f3cf00`, `679959ac`, `b4ff51eb` (внешние ссылки, embedded files, Reference XObject, OPI, Web Capture, пароли в URL) | не берём | Валидация и отчёты о ссылках, см. правила. |
| `7b3a487b`, `99222dd1`, `4aaf161b`, `422f8fc1`, `00187b64`, `05b59d32`, `71d5830a`, `91b7ff81`, `7ab33925`, `ba94f4a3`, `21d22d7f`, `b1be5edc` (валидация media, Sound, Movie, Rendition, RichMedia, 3D, активное содержимое) | не берём | Строже валидация, см. правила. |
| `10aa9df4`, `d0282ae5` (перенос тестов инвентаря, референсы TIFF) | не берём | Тестовое обслуживание upstream. |
| `2f7a89a0` bump version | не берём | Версию форка ведём сами. |

## Upstream 2f7a89a0..b41a41d5 (11 October 2026)

Reviewed 91 commits: 50 accepted fully or partially, 41 omitted/deferred. Partial ports below are intentional compatibility decisions, not unhandled conflicts. Resource caps retain their existing values; no cumulative read budget, password-profile dependency, stricter annotation writer or input-version gate is added. The fork's common inline-image normalization, default color spaces, file-backed streams, fingerprints and pools remain authoritative.

| Source | Decision | Reason / adaptation |
|---|---|---|
| `13fbc914` fix: preserve inherited CropBox during page extraction | Accept | Preserve inherited CropBox in extracted pages. |
| `d6056ff1` fix: use SASLprep consistently for AES-256 passwords | Omit | Defer: changes Unicode password normalization/read/write semantics and adds stringprep. Need a password compatibility corpus before replacing the existing accepted-password contract. |
| `22142d17` fix: hash only the 48-byte U entry for AES-256 owners | Accept | Use the defined 48-byte U prefix for R5/R6 owner authentication; retain padded input acceptance. |
| `67ea897e` fix: bound repeated indirect-object parsing during Read | Omit | Reject: new cumulative 8*input+MaxObjectBytes parsing budget can reject previously processed aliased/repaired input. |
| `2e257c7d` fix: reject annotations with missing rectangles in strict mode | Omit | Reject/defer: additional validation is not needed for processing and is not proven to preserve previously accepted inputs, including relaxed-mode inputs. |
| `6f6cbc76` fix: default missing CropBox to MediaBox for box assignments | Accept | Use the existing MediaBox default when assigning CropBox-based boxes. |
| `9a9677b0` fix: handle unknown CCITT rows and reject zero columns | Accept | Decode unknown CCITT height; reject zero-width rows that otherwise cannot make progress. |
| `df064c4b` fix: require default appearance for choice fields | Omit | Reject: adds default-appearance requirements for choice fields. |
| `817e882e` fix: reject overlapping TrueType cmap ranges | Accept | Prevent repeated/overlapping cmap ranges from amplifying font installation work; input PDF reading is unaffected. |
| `74d8ad04` fix: handle missing resource categories during consolidation | Adapt | Adapt resource dereferencing/pruning to the fork traversal; retain default color spaces and nearest resource scope. |
| `1b2f4785` refactor: simplify form field DA validation | Omit | Skip: refactor of the omitted form default-appearance validation. |
| `bbc0947c` fix named stream Crypt filter decryption | Accept | Honor explicit stream crypt filters, including file-backed writer inputs. |
| `7150e776` fix DCT prefix decoding and decoded-byte limits | Accept | Correct prefix decoding and enforce the existing decoded-byte cap, not a new limit. |
| `0c1c1415` fix object keyword scanning across strings and comments | Accept | Do not interpret markers inside strings/comments; keep zero-offset marker state distinct. |
| `f7a750c3` fix trailing xref marker panic | Accept | Bounds-check a trailing xref marker instead of panicking. |
| `4b95bdeb` fix encryption revision checks and extraction permissions | Omit | Defer: changes extraction permission requirements and rejects additional encryption V/R combinations. |
| `6d3a0058` fix recursive stream filter references | Adapt | Detect circular active-object reads and retain required-context errors before accessing context values. Do not add the upstream distinct-object nesting limit. Classify cycles as structural input damage for the shared recovery owner. |
| `00b39d23` fix flate predictor encoding | Accept | Apply predictors before Flate encoding; retain exported zlib pools. Non-8-bit TIFF encoding remains unsupported instead of emitting incorrect samples. |
| `e6394e67` fix button field classification across form commands | Accept | Classify buttons using inherited flags and update all widgets. |
| `af0cf7ae` fix form export annotation handling | Accept | Handle annotation objects correctly during form export. |
| `39751945` fix empty single-select list box fill | Accept | Fill an empty single-select list without a false selection. |
| `fc30a91d` fix unnamed form field JSON matching | Accept | Match unnamed form fields by their existing identifiers. |
| `69050fe7` fix form field inheritance and choice handling | Accept | Preserve field inheritance, choice indices and separate widget appearances. |
| `650b8f3a` fix PDF header scanning across block boundaries | Accept | Find PDF headers across read boundaries. |
| `9a18887d` fix hexadecimal string whitespace handling | Accept | Ignore PDF whitespace within hex digit pairs without inserting bytes. |
| `7efef951` fix Identity crypt filters for streams and strings | Accept | Keep stream/string Identity defaults independent on read/write. |
| `a588c0e6` fix inherited page attributes in zip merging | Accept | Materialize inherited page attributes before changing page parents. |
| `59af8045` fix required metadata stream entries in strict validation | Omit | Reject/defer: additional validation is not needed for processing and is not proven to preserve previously accepted inputs, including relaxed-mode inputs. |
| `950d9296` fix surviving subtree deletion during name tree collapse | Accept | Do not delete a surviving name-tree subtree during collapse. |
| `fb56a676` fix invalid object stream entries causing nil-pointer panics | Accept | Return an object-stream error instead of dereferencing an invalid entry. |
| `5cadb93f` fix stream deduplication and retained reference generations | Adapt | Compare stream dictionaries and encoded bytes; preserve retained generations. Reuse EqualRaw for file sources; null equality was already fixed. |
| `97ac21b0` fix content resource parsing and inline image boundaries | Adapt | Partial: decode resource-name escapes, skip dictionary comments and parse inline Indexed color-space arrays. Preserve malformed-name tolerance and existing EI framing; do not add strict-mode rejection or a second image-framing mechanism. |
| `3230fd3e` fix PDF real number parsing and relaxed rectangle repair | Omit | Defer: rejects previously tolerated numeric syntax and rewrites missing rectangle coordinates; do not bundle with unrelated placeholder repair. |
| `e3277f58` fix whitespace separation in indirect object headers | Accept | Keep object/generation numbers separated by PDF whitespace. |
| `6ab44661` fix PDF tokenization to use PDF whitespace bytes | Omit | Reject: narrows previously accepted Unicode whitespace to six PDF bytes. |
| `532ba314` fix relaxed skipping of malformed streams | Adapt | Keep fatal cycle errors only. Do not return nil on stream-construction failure: backend regression tests show that referenced malformed content is then silently dropped and page preparation returns success. Retain the existing rejection path instead of producing an empty document. |
| `e518b4c9` fix bookmark removal preserving named destinations | Accept | Keep named destinations when removing/replacing bookmarks. |
| `d730f450` fix crop box removal with inherited page boundaries | Accept | Respect inherited boundaries when removing CropBox. |
| `e2b939f4` fix rendering of packed CMYK image samples | Accept | Render packed/16-bit CMYK samples with checked row geometry. |
| `37a9bc7c` fix panic on incomplete Indexed color spaces | Accept | Guard incomplete Indexed color-space arrays before accessing entries. |
| `e09ed8fb` fix xref repair limits and state after skipped objects | Accept | Resume repair outside skipped objects. Repair scanners now honor the existing MaxObjectBytes setting (default unchanged at 64 MB), including smaller explicitly configured caps. |
| `d6baff19` fix premultiplied colors in RGBA image import | Accept | Unpremultiply imported RGBA samples before writing color/alpha planes. |
| `896617f2` fix CRLF handling across scanner reads | Accept | Keep CRLF intact across scanner reads. |
| `46953e1c` fix required structure element type validation | Omit | Reject/defer: additional validation is not needed for processing and is not proven to preserve previously accepted inputs, including relaxed-mode inputs. |
| `bad22e68` fix text field MaxLen character counting | Accept | Count form MaxLen in Unicode code points. |
| `6091d114` fix TIFF predictor decoding for non-8-bit components | Accept | Decode TIFF component arithmetic at 1/2/4/8/16 bits; update both buffered and streaming consumers of the shared algorithm. |
| `71d4ddf6` fix ToUnicode mappings beyond 100 glyphs | Accept | Write all ToUnicode mappings in correctly sized blocks. |
| `cc0af77d` fix zero-valued first keys in tree validation | Omit | Defer: changes tree range validation; keep with a separately reviewed name/number-tree compatibility update. |
| `9e2bbb1a` fix strict tree key ordering validation | Omit | Reject: new strict ordering rejection for name/number trees. |
| `079e87f3` fix TrueType widths for WinAnsi character codes | Accept | Map WinAnsi character codes before selecting glyph widths. |
| `83526e7e` fix TrueType bold flag detection | Accept | Read the actual TrueType bold flag. |
| `2fb2b78f` fix XMP date parsing | Accept | Accept supported partial XMP dates/time zones/fractional seconds. |
| `a4c764c2` fix unknown cross-reference stream entry types | Accept | Initialize unknown xref stream types as null/free entries instead of panicking. |
| `3a9a23d8` fix recovery marker whitespace detection | Omit | Reject: depends on the narrower whitespace policy. |
| `4f96edde` fix content parser whitespace boundaries | Omit | Reject: narrows content whitespace, including previously tolerated bytes. |
| `6d4e3be3` fix #1494: slideshow validation with empty resource trees | Omit | Skip: compatibility repair for multimedia validation previously excluded from this fork. |
| `d341febc` fix #1503, #1504: preserve legacy name tree values | Omit | Defer: changes legacy name-tree value validation; keep with the separately reviewed tree contract. |
| `f460b05f` fix #1508: preserve shared PDF stamp resources | Accept | Clone shared PDF-stamp resources before migration. |
| `125f9eaf` fix #1497: reuse JavaScript name tree action traversal | Omit | Skip: JavaScript/action traversal and reporting excluded by policy. |
| `979c0747` fix #1496: reuse shared Web Capture validation | Omit | Skip: Web Capture validation excluded by policy. |
| `f08a7780` fix #1495: reuse Rendition page and annotation lookups | Omit | Skip: Rendition validation excluded by policy. |
| `7881a0f1` fix #1498, #1499: reuse shared 3D and RichMedia validation | Omit | Skip: 3D/RichMedia validation excluded by policy. |
| `068ff61d` fix #1500: parse large CRLs without per-value BER allocations | Accept | Scan definite BER subtrees without per-value allocations, retaining existing resource limits. |
| `a458bdcb` fix #1501: repair invalid trailer Size in relaxed mode | Accept | Repair invalid trailer Size in relaxed mode without weakening actual object-count limits. |
| `3a4bc841` fix #1505: restore relaxed media clip data compatibility | Omit | Skip: media-clip validation repair for previously excluded checks. |
| `8ca5a507` fix: clean up command test temporary directories before exit | Omit | Skip: upstream CLI test-process housekeeping, unrelated to the service/fork contracts. |
| `2e2c57eb` fix #1506: restore multimedia compatibility and correct action reports | Omit | Skip: multimedia/action-report changes for previously excluded validation. |
| `7417c636` fix inline image parsing for PDF 2.0 input | Omit | Defer: combines PDF 2.0 Length framing with new rejection rules; needs one coherent extension of the fork's normalization and fallback parsing owners. |
| `71d91c25` fix name tree byte ordering while preserving decoded key lookup | Omit | Defer: changes raw/decoded name-tree key identity and validation together; do not mix with the rejected text-decoding contract change. |
| `5610c259` fix explicit crypt filter method compatibility | Omit | Reject: adds explicit crypt-method compatibility rejection; retain the accepted crypt decoding fixes without this validation. |
| `74de8dcf` fix input version checks after catalog discovery | Omit | Reject as a whole: adds post-catalog version/encryption rejection, including additional required crypt-filter Length entries. |
| `20003608` enforce validation mode for duplicate keys and keyword casing | Omit | Reject: changes strict duplicate-key/keyword acceptance and relaxed duplicate/null semantics. |
| `07093853` fix text string decoding and preserve hex bytes | Omit | Reject as a whole: stops accepting unmarked UTF-8 text as UTF-8. A standalone hex-byte preservation change needs separate compatibility coverage. |
| `9e924ba9` fix standard crypt-filter length units | Adapt | Partial/adapted: write standard crypt-filter lengths in bytes; accept canonical bytes while preserving previously accepted legacy bit lengths, including strict pre-2.0 input. |
| `472e8f18` preserve catalog object graphs and renumber compressed merge references | Accept | Preserve catalog reference graphs and patch references inside lazy compressed merge objects. |
| `d6d64f83` complete standard font metrics when writing PDF 2.0 | Omit | Defer: broad PDF 2.0 font-writing compliance changes are separate from compatible read/image repairs. |
| `2d43da15` accept NUL whitespace in ASCIIHexDecode | Accept | Accept PDF NUL whitespace in ASCIIHex data. |
| `581f4cd2` fix annotation border dash array length validation | Accept | Accept arbitrary valid dash-array lengths. Also accept the standard empty (solid-line) array; retain negative/all-zero rejection. |
| `d73a855e` fix crypt stage handling in stream filter pipelines | Adapt | Skip the security-handler-owned Crypt stage in sample encode/decode, including WriteDecodedTo and file sources. |
| `3a63c5e1` validate image dictionary consistency with relaxed compatibility | Omit | Reject/defer: additional validation is not needed for processing and is not proven to preserve previously accepted inputs, including relaxed-mode inputs. |
| `cba81604` enforce required PDF 2.0 annotation appearances | Omit | Reject/defer: additional validation is not needed for processing and is not proven to preserve previously accepted inputs, including relaxed-mode inputs. |
| `33353f92` validate PDF 2.0 graphics state entries | Omit | Reject/defer: additional validation is not needed for processing and is not proven to preserve previously accepted inputs, including relaxed-mode inputs. |
| `24c2998a` accept custom PDF 2.0 associated file relationships | Accept | Accept custom PDF 2.0 AFRelationship names without importing previously excluded validation. |
| `16a98375` validate graphics values with relaxed compatibility | Omit | Reject/defer: additional validation is not needed for processing and is not proven to preserve previously accepted inputs, including relaxed-mode inputs. |
| `5eb5a763` validate Type 2 function domains with relaxed compatibility | Omit | Reject/defer: additional validation is not needed for processing and is not proven to preserve previously accepted inputs, including relaxed-mode inputs. |
| `4a5370f2` validate destination operands with relaxed compatibility | Omit | Reject/defer: additional validation is not needed for processing and is not proven to preserve previously accepted inputs, including relaxed-mode inputs. |
| `23c7fad0` validate page labels with relaxed compatibility | Omit | Reject/defer: additional validation is not needed for processing and is not proven to preserve previously accepted inputs, including relaxed-mode inputs. |
| `7543e7d8` validate CalGray values with relaxed compatibility | Omit | Reject/defer: additional validation is not needed for processing and is not proven to preserve previously accepted inputs, including relaxed-mode inputs. |
| `a9b5201a` validate Indexed lookup lengths with relaxed compatibility | Omit | Reject/defer: additional validation is not needed for processing and is not proven to preserve previously accepted inputs, including relaxed-mode inputs. |
| `b38e46cc` validate AESV3 crypt-filter lengths with relaxed compatibility | Omit | Reject/defer: additional validation is not needed for processing and is not proven to preserve previously accepted inputs, including relaxed-mode inputs. |
| `b41a41d5` bump version | Omit | Reject: keep the fork's own version; upstream CI latest-toolchain setting is not a runtime repair. |
