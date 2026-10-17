#include "ginkgo_kws.h"
#include <algorithm>
#include <cmath>
#include <cstdio>
#include <limits>
#include <memory>
#include <mutex>
#include <stdexcept>
#include <string>
#include <vector>
#include "sherpa-onnx/csrc/keyword-spotter.h"
#include "sherpa-onnx/csrc/online-transducer-model.h"
#include "sherpa-onnx/csrc/symbol-table.h"
#include "sherpa-onnx/csrc/transducer-keyword-decoder.h"

namespace so = sherpa_onnx;
namespace {
constexpr int kD=8, kShift=32, kChunk=45;
// Bounded continuous lifetime: fail closed and require a NEW helper session.
// This stays far below every native int32 feature/sample index horizon.
constexpr int64_t kMaxSamples=16000LL*60*60*8;
constexpr int64_t kRingSamples=16000*35;
const std::vector<int64_t> kIDs{36,32,35,38,48,44,50};
const std::vector<std::string> kPhones{"HH","EY1","G","IH1","NG","K","OW1"};
void Require(bool value,const char *message) { if (!value) throw std::runtime_error(message); }
void Error(char *out,uint32_t size,const char *message) noexcept {
  if (out && size) std::snprintf(out,size,"%s",message);
}
}
struct ginkgo_kws_v1 {
  std::mutex mutex;
  std::unique_ptr<so::KeywordSpotter> spotter;
  std::unique_ptr<so::OnlineStream> stream;
  std::string label;
  int64_t accepted=0, global=0, previous=-1;
  bool failed=false, finished=false;

  void Drain(ginkgo_kws_v1_event *events,uint32_t capacity,uint32_t *count) {
    for (;;) {
      int cursor=stream->GetNumProcessedFrames();
      Require(cursor>=0 && cursor<INT32_MAX-kChunk && global<INT32_MAX-kD,
              "native clock horizon"); // BEFORE readiness's native addition
      if (!spotter->IsReady(stream.get())) break;
      auto before=stream->GetKeywordResult(false); // copy, never hold native reference
      Require(before.frame_offset>=0 && before.frame_offset<=global,"invalid raw epoch");
      bool automatic=before.num_trailing_blanks*0.04>1.5;
      spotter->DecodeStream(stream.get()); // exceptions poison entire stream
      auto raw=stream->GetKeywordResult(false);
      Require(stream->GetNumProcessedFrames()==cursor+kShift &&
              raw.frame_offset==(automatic?kD:before.frame_offset+kD),"native progress mismatch");
      global+=kD; // exactly once, only after a successful verified Decode
      auto result=spotter->GetResult(stream.get()); // exactly one immediate filtered poll
      if (result.keyword.empty()) {
        if (!raw.tokens.empty()) spotter->Reset(stream.get());
        continue;
      }
      // Reset even an unexpected/rejected nonempty result before validation.
      spotter->Reset(stream.get());
      Require(stream->GetNumProcessedFrames()==cursor+kShift &&
              stream->GetKeywordResult(false).frame_offset==0,"native reset mismatch");
      Require(raw.tokens==kIDs && raw.timestamps.size()==7 && result.tokens==kPhones &&
              result.timestamps.size()==7 && result.start_time==0 && raw.keyword==label &&
              result.keyword==(label.empty()?"HEY1GIH1NGKOW1":label),"native result identity mismatch");
      const int64_t origin=global-raw.frame_offset;
      Require(origin>=0,"negative native origin");
      ginkgo_kws_v1_event event{};
      for (int i=0;i<7;++i) {
        Require(raw.timestamps[i]>=0 && result.timestamps[i]==float(raw.timestamps[i])*float(.04),
                "filtered/raw timestamp mismatch");
        event.frames[i]=origin+raw.timestamps[i];
        Require(event.frames[i]>=0 && event.frames[i]<global &&
                (i==0?event.frames[i]>previous:event.frames[i]>event.frames[i-1]),"invalid native token order");
      }
      Require(event.frames[0]*640>=std::max<int64_t>(0,accepted-kRingSamples) &&
              event.frames[6]*640<accepted,"native event outside retained window");
      previous=event.frames[6];
      event.notification_sample=accepted;
      Require(*count<capacity,"native event capacity exceeded");
      events[(*count)++]=event;
    }
  }
};
extern "C" uint32_t ginkgo_kws_v1_abi(void) { return 1; }
extern "C" int ginkgo_kws_v1_create(const char *e,const char *d,const char *j,
    const char *t,const char *alias,ginkgo_kws_v1 **out,char *error,uint32_t size) {
  if (out) *out=nullptr;
  try {
    Require(out && e && d && j && t && alias,"invalid native arguments");
    std::string label(alias);
    Require(label.size()<=64 && std::all_of(label.begin(),label.end(),[](unsigned char c) {
      return (c>='a'&&c<='z')||(c>='A'&&c<='Z')||(c>='0'&&c<='9')||c=='_'||c=='-';
    }),"invalid native label");
    so::SymbolTable symbols(t);
    for (int i=0;i<7;++i) Require(symbols.Contains(int32_t(kIDs[i])) && symbols[int32_t(kIDs[i])]==kPhones[i],"unsupported phone inventory");
    so::KeywordSpotterConfig c;
    c.model_config.transducer.encoder=e;c.model_config.transducer.decoder=d;c.model_config.transducer.joiner=j;
    c.model_config.tokens=t;c.model_config.num_threads=1;c.model_config.provider_config.provider="cpu";
    c.max_active_paths=16;c.num_trailing_blanks=1;c.keywords_score=1;c.keywords_threshold=.25;
    c.keywords_buf="HH EY1 G IH1 NG K OW1"+(label.empty()?std::string():" @"+label);
    Require(c.Validate(),"invalid native profile");
    // Attest actual encoder tensor on identically hash-verified sealed artifacts.
    // Stock spotter hides its encoder; per-Decode progress below binds the same
    // fixed D/profile to the owned stream, not an inferred C-API reset origin.
    auto model=so::OnlineTransducerModel::Create(c.model_config);model->SetFeatureDim(80);
    Require(model->ChunkSize()==kChunk && model->ChunkShift()==kShift && c.feat_config.frame_shift_ms==10,"unsupported encoder geometry");
    auto memory=Ort::MemoryInfo::CreateCpu(OrtDeviceAllocator,OrtMemTypeDefault);
    std::vector<float> zeros(kChunk*80);int64_t shape[]{1,kChunk,80},one=1,processed=0;
    auto x=Ort::Value::CreateTensor(memory,zeros.data(),zeros.size(),shape,3);
    auto p=Ort::Value::CreateTensor(memory,&processed,1,&one,1);
    auto encoded=model->RunEncoder(std::move(x),model->GetEncoderInitStates(),std::move(p));
    Require(encoded.first.GetTensorTypeAndShapeInfo().GetShape()==std::vector<int64_t>({1,kD,320}),"unsupported encoder output");
    auto owned=std::make_unique<ginkgo_kws_v1>();
    owned->label=label;
    owned->spotter=std::make_unique<so::KeywordSpotter>(c);owned->stream=owned->spotter->CreateStream();
    *out=owned.release();return 0;
  } catch (const std::exception &e) { Error(error,size,e.what());return 1; }
    catch (...) { Error(error,size,"native KWS initialization failed");return 1; }
}
static int Run(ginkgo_kws_v1 *s,const float *pcm,uint32_t n,bool finish,
    ginkgo_kws_v1_event *events,uint32_t capacity,uint32_t *count,char *error,uint32_t size) {
  if (count) *count=0;
  if (!s) { Error(error,size,"native KWS closed");return 1; }
  std::unique_lock<std::mutex> lock;
  try {
    lock=std::unique_lock<std::mutex>(s->mutex);
    Require(!s->failed && !s->finished,"native KWS stream unusable");
    Require(events && count && capacity>0 && capacity<=GINKGO_KWS_V1_MAX_EVENTS,"invalid native event buffer");
    Require(n<=GINKGO_KWS_V1_MAX_INPUT && (n==0||pcm) && (finish?n==0:n>0),"invalid native input length");
    Require(s->accepted<=kMaxSamples-int64_t(n),"native session lifetime exceeded");
    for (uint32_t i=0;i<n;++i) Require(std::isfinite(pcm[i]) && pcm[i]>=-1 && pcm[i]<=1,"invalid native PCM");
    // Bound feature/sample arithmetic before native Accept/readiness/Decode.
    Require(s->stream->GetNumProcessedFrames()>=0 &&
            s->stream->GetNumProcessedFrames()<INT32_MAX-kChunk,"native feature horizon");
    if (finish) { s->finished=true;s->stream->InputFinished(); }
    else { s->stream->AcceptWaveform(16000,pcm,n);s->accepted+=n; }
    s->Drain(events,capacity,count);return 0;
  } catch (const std::exception &e) { if(lock.owns_lock())s->failed=true;if(count)*count=0;Error(error,size,e.what());return 1; }
    catch (...) { if(lock.owns_lock())s->failed=true;if(count)*count=0;Error(error,size,"native KWS stream failed");return 1; }
}
extern "C" int ginkgo_kws_v1_accept(ginkgo_kws_v1 *s,const float *p,uint32_t n,ginkgo_kws_v1_event *e,uint32_t cap,uint32_t *count,char *error,uint32_t size) {
  return Run(s,p,n,false,e,cap,count,error,size);
}
extern "C" int ginkgo_kws_v1_finish(ginkgo_kws_v1 *s,ginkgo_kws_v1_event *e,uint32_t cap,uint32_t *count,char *error,uint32_t size) {
  return Run(s,nullptr,0,true,e,cap,count,error,size);
}
extern "C" void ginkgo_kws_v1_destroy(ginkgo_kws_v1 *s) { delete s; }
