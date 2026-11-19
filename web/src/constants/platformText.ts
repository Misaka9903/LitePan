import { computed } from "vue";
import { useNetworkInfo } from "@/composables/usePlatform";

// 平台差异文案集中在这里：两个平台各写一份完整文案，
// 组件只取键值，不再散落 isWindows ? A : B。
const WINDOWS_TEXTS = {
  btPortTooltip: "用于磁力/BT 下载连接其他节点",
  uploadPathLabel: "本机路径",
  uploadPathPlaceholder: "D:\\LitePan\\updatefiles",
  uploadPathHelpTitle: "本机路径说明",
  uploadPathHelpBody:
    "本机上的文件夹路径，例如 <code>D:\\LitePan\\updatefiles</code>。<br>前台从服务器上传时按左侧标签浏览，不会暴露其他路径。",
  uploadPathToast: "请填写标签名和本机路径",
};

const DOCKER_TEXTS = {
  btPortTooltip:
    "用于磁力/BT 下载连接其他节点；Docker Bridge 网络需映射同一 TCP/UDP 端口，Host 网络无需映射",
  uploadPathLabel: "容器内路径",
  uploadPathPlaceholder: "/app/data/updatefiles",
  uploadPathHelpTitle: "容器内路径说明",
  uploadPathHelpBody:
    "docker-compose 中映射到容器的路径（容器内那一侧），例如 <code>/app/data/updatefiles</code>。<br>前台从服务器上传时按左侧标签浏览，不会暴露服务器其他路径。",
  uploadPathToast: "请填写标签名和以 / 开头的容器内路径",
};

export function usePlatformText() {
  const { isWindows } = useNetworkInfo();
  return computed(() => (isWindows.value ? WINDOWS_TEXTS : DOCKER_TEXTS));
}
