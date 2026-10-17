#ifndef GINKGO_KWS_V1_H
#define GINKGO_KWS_V1_H
#include <stdint.h>
#ifdef __cplusplus
extern "C" {
#endif
#define GINKGO_KWS_API __attribute__((visibility("default")))
typedef struct ginkgo_kws_v1 ginkgo_kws_v1;
enum { GINKGO_KWS_V1_MAX_EVENTS = 32, GINKGO_KWS_V1_MAX_INPUT = 16000 };
typedef struct {
  int64_t frames[7]; /* integer emission grid, NOT phonetic boundaries */
  int64_t notification_sample;
} ginkgo_kws_v1_event;
/* Owned Go caller must attest exact profile hashes and supply sealed read-only
 * model descriptors. Paths/text are copied during create, never retained Go data.
 * Errors use caller-owned buffers, no exception crosses this ABI. */
GINKGO_KWS_API int ginkgo_kws_v1_create(const char *encoder, const char *decoder,
    const char *joiner, const char *tokens, const char *alias,
    ginkgo_kws_v1 **out, char *error, uint32_t error_size);
/* Serialized per stream; any error permanently invalidates it. events/count
 * are caller-owned; partial output on failure MUST NOT be consumed. */
GINKGO_KWS_API int ginkgo_kws_v1_accept(ginkgo_kws_v1 *, const float *, uint32_t n,
    ginkgo_kws_v1_event *events, uint32_t capacity, uint32_t *count,
    char *error, uint32_t error_size);
/* Terminal input finish/drain; stream cannot accept again. */
GINKGO_KWS_API int ginkgo_kws_v1_finish(ginkgo_kws_v1 *, ginkgo_kws_v1_event *,
    uint32_t capacity, uint32_t *count, char *error, uint32_t error_size);
/* Null is safe. Caller must zero its pointer: no generic stream reset/reuse. */
GINKGO_KWS_API void ginkgo_kws_v1_destroy(ginkgo_kws_v1 *);
GINKGO_KWS_API uint32_t ginkgo_kws_v1_abi(void);
#ifdef __cplusplus
}
#endif
#endif
