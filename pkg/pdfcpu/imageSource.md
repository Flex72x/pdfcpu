# Original image sources

`ExtractImageSource` is separate from `ExtractImage`/`RenderImage`. It reads original encoded data or raster samples, without page rendering, external masks, Matte, Decode arrays or external ICC transforms. Existing rendering APIs retain their behavior.

JPEG, JP2 and J2K remain encoded. Only filters preceding the terminal image filter are decoded, with the document's decode limit. JPEG issuance checks its header, not all samples. Consumers that need to decode or convert remain responsible for validating complete codec data. `ExtractEncodedImageSource` intentionally omits JPEG header validation so an existing consumer can apply its own safe repair policy.

Raster samples retain dimensions, row padding and bit depth. Bits are MSB-first; 16-bit components are big-endian. Supported component descriptions are DeviceGray, DeviceRGB, DeviceCMYK, CalGray/CalRGB component layouts, and ICCBased N=1/3/4 without profile transformation. Indexed sources contain packed indexes and a component lookup table. Representations requiring colorant functions or Lab interpretation are unsupported.

Returned slices may alias PDF buffers. Callers must keep them immutable and release operation-owned buffers after use. Source objects, decoded caches and dependencies are not modified. No native decoder, standalone file encoding or subprocess belongs to this API.

JBIG2 sources include decoded JBIG2Globals. `NewJBIG2ImageContext` builds an independent one-page document containing only the selected image and globals, allowing a consumer-owned external decoder to avoid unrelated page images.

Use `errors.Is` to distinguish invalid sources, unsupported representations and resource-limit failures. Cancellation and API precondition errors keep their identities. `IsStructuralReadError` conservatively recognizes reader errors eligible for structural recovery; it excludes image failures, operational errors and resource-limit errors.
