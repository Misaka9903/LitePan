import { computed, ref } from "vue";
import { fetchNetworkAddresses } from "@/api/settings";

// 首屏平台由后端在 index.html 里直接注入，前端第一帧就能判定，
// 不用等接口返回，两个平台都不会闪烁。接口只用来补局域网 IP。
function detectPlatform(): string {
  if (typeof window === "undefined") return "";
  const injected = (window as unknown as { __PLATFORM__?: string }).__PLATFORM__;
  return typeof injected === "string" ? injected : "";
}

const platform = ref(detectPlatform());
const ips = ref<string[]>([]);
let started = false;

function load() {
  if (started) return;
  started = true;
  fetchNetworkAddresses()
    .then((d) => {
      platform.value = d.platform || "";
      ips.value = d.ips ?? [];
    })
    .catch(() => {});
}

export function useNetworkInfo() {
  load();
  return {
    platform,
    ips,
    // 未知时按 Windows 处理，避免 FUSE 相关入口先显示再消失
    isWindows: computed(() => platform.value === "windows" || platform.value === ""),
  };
}

// 兼容只关心平台判断的调用方
export const usePlatform = useNetworkInfo;
