// Tests the very same private guards without constructing/loading any model.
#include "ginkgo_kws.cc"
#include <cassert>

int main() {
  char error[256]{};
  ginkgo_kws_v1_event events[32]{};
  uint32_t count=99;
  float zero=0;
  assert(ginkgo_kws_v1_abi()==1);
  assert(ginkgo_kws_v1_accept(nullptr,&zero,1,events,32,&count,error,256)!=0 && count==0);
  ginkgo_kws_v1 *out=nullptr;
  assert(ginkgo_kws_v1_create(nullptr,nullptr,nullptr,nullptr,nullptr,&out,error,256)!=0 && !out);
  for (int kind=0;kind<7;++kind) {
    ginkgo_kws_v1 stream;
    float sample=kind==4?std::numeric_limits<float>::quiet_NaN():kind==5?2:0;
    if (kind==6) stream.accepted=kMaxSamples;
    assert(ginkgo_kws_v1_accept(&stream,kind==3?nullptr:&sample,
      kind==0?0:kind==1?16001:1,events,kind==2?33:32,&count,error,256)!=0);
    assert(stream.failed && count==0 && error[0]);
    assert(ginkgo_kws_v1_accept(&stream,&zero,1,events,32,&count,error,256)!=0);
  }
  ginkgo_kws_v1_destroy(nullptr);
}
