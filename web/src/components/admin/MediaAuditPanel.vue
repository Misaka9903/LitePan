<script setup lang="ts">
import { computed, nextTick, onBeforeUnmount, onMounted, ref } from "vue";
import { storeToRefs } from "pinia";
import { getApiErrorMessage } from "@/api/client";
import type { MediaAuditGroup, MediaAuditMoveResult, MediaAuditReport } from "@/api/mediaOrganize";
import {
  fetchMediaOrganizeAudit,
  fetchMediaOrganizeAuditJob,
  fetchMediaOrganizeSettings,
  moveMediaOrganizeAudit,
  runMediaOrganizeAudit,
  startMediaOrganizeAudit,
} from "@/api/mediaOrganize";
import { useAccountsStore } from "@/stores/accounts";
import AppButton from "@/components/base/AppButton.vue";
import AccountFolderField from "@/components/admin/AccountFolderField.vue";
import AdminEmptyState from "@/components/admin/AdminEmptyState.vue";
import FolderPickerModal from "@/components/file/FolderPickerModal.vue";
import { toast } from "@/composables/useToast";
import { formatSize } from "@/utils/format";

const accountsStore = useAccountsStore();
const { accounts } = storeToRefs(accountsStore);

const activeAccounts = computed(() => accounts.value.filter((a) => a.is_active));

const pickerOpen = ref(false);
const movePickerOpen = ref(false);
const moveTargetId = ref("");
const moveTargetName = ref("");
const moving = ref(false);
const moveResult = ref<MediaAuditMoveResult | null>(null);
const rootId = ref("");
const rootName = ref("");
const accountId = ref<number>(0);
const scanning = ref(false);
const jobLogs = ref<Array<{ time: string; message: string }>>([]);
// 进行中自动展开，完成后折叠保留，方便回看。
const logOpen = ref(false);
/** 归集移动的逐条勾选；默认勾上「有问题的」作品 */
const selected = ref<Record<string, boolean>>({});
const selectedCount = computed(() => Object.values(selected.value).filter(Boolean).length);
const logBox = ref<HTMLElement | null>(null);
let pollTimer: number | null = null;
const report = ref<MediaAuditReport | null>(null);
const onlyBad = ref(true);
const unit = ref<"show" | "season">("show");
/** 判定项，来自「目录整理 → 媒体信息顺序」 */
const requiredTags = ref<string[]>(["screen_size", "video_codec", "audio_codec"]);
const checkMediaInfo = ref(true);
const checkSeasons = ref(true);
const checkEpisodes = ref(true);
const checkFranchise = ref(true);

const missingLabels: Record<string, string> = {
  screen_size: "分辨率",
  hdr: "动态范围",
  bit_depth: "色深",
  video_codec: "视频编码",
  audio_codec: "音频编码",
  audio_channels: "声道",
  frame_rate: "帧率",
};

function labelOf(key: string) {
  return missingLabels[key] ?? key;
}

const requiredLabel = computed(() => requiredTags.value.map(labelOf).join(" · "));

/** 某一项缺了几集（没数据时返回 0） */
function missingFiles(group: MediaAuditGroup, key: string) {
  return group.missing_files?.[key] ?? 0;
}

/** 部分缺：有集带、有集不带；整季都缺时文件数等于总集数 */
function isPartialMissing(group: MediaAuditGroup, key: string) {
  const miss = missingFiles(group, key);
  return miss > 0 && miss < (group.missing_total ?? 0);
}

function lastSegment(path?: string) {
  if (!path) {
    return "";
  }
  const parts = path.split("/").filter(Boolean);
  return parts[parts.length - 1] ?? "";
}

/** 每个缺失标签点上去能看到的解释（多季时按季拆开） */
function missingHint(group: MediaAuditGroup, key: string) {
  const label = labelOf(key);
  const perSeason = (group.seasons ?? [])
    .map((s) => ({ name: lastSegment(s.dir_path), miss: s.missing_files?.[key] ?? 0, total: s.total ?? 0 }))
    .filter((x) => x.miss > 0);
  if (perSeason.length === 0) {
    return `文件名里解析不出${label}`;
  }
  if (perSeason.length === 1) {
    const only = perSeason[0];
    return only.miss >= only.total
      ? `整季（${only.total} 集）都解析不出${label}，整理后这一季都没有这一段`
      : `${only.total} 集里有 ${only.miss} 集解析不出${label}，整理后这些集的名字会缺这一段`;
  }
  const detail = perSeason.map((x) => `${x.name} ${x.miss}/${x.total}`).join("；");
  return `${detail} 解析不出${label}`;
}

function openPicker() {
  pickerOpen.value = true;
}

function onFolderPicked(payload: { accountId: number; parentId: string; path: string }) {
  accountId.value = payload.accountId;
  rootId.value = payload.parentId;
  rootName.value = payload.path || "/";
  pickerOpen.value = false;
  report.value = null;
}

const anyCheck = computed(
  () => checkMediaInfo.value || checkSeasons.value || checkEpisodes.value || checkFranchise.value,
);

const payload = computed(() => ({
  account_id: accountId.value,
  root_id: rootId.value,
  root_name: rootName.value,
  unit: unit.value,
  check_media_info: checkMediaInfo.value,
  check_seasons: checkSeasons.value,
  check_episodes: checkEpisodes.value,
  check_franchise: checkFranchise.value,
}));

function stopPolling() {
  if (pollTimer !== null) {
    window.clearInterval(pollTimer);
    pollTimer = null;
  }
}

function resetSelection() {
  const map: Record<string, boolean> = {};
  for (const group of (report.value?.groups ?? []).map(normalizeGroup)) {
    if (isBad(group)) {
      map[group.id] = true;
    }
  }
  selected.value = map;
}

async function applyReport(data: Awaited<ReturnType<typeof runMediaOrganizeAudit>>) {
  report.value = data;
  resetSelection();
  const bad = data.summary.bad_groups ?? 0;
  const incomplete = data.summary.incomplete_groups ?? 0;
  if (bad === 0 && incomplete === 0) {
    toast.success(`体检完成：${data.summary.total_groups} 部全部齐全`);
  } else if (bad > 0) {
    const extra = incomplete > 0 ? `，另有 ${incomplete} 部缺季集` : "";
    toast.success(`体检完成：${bad} 部信息不全${extra}`);
  } else {
    toast.success(`体检完成：${incomplete} 部缺季集`);
  }
}

/** 异步体检：轮询进度日志，完成后取报告 */
async function startAudit() {
  if (!rootId.value) {
    toast.error("请先选择要体检的目录");
    return;
  }
  if (!anyCheck.value) {
    toast.error("至少选择一项检查");
    return;
  }
  scanning.value = true;
  jobLogs.value = [];
  logOpen.value = false;
  report.value = null;
  try {
    const started = await startMediaOrganizeAudit(payload.value);
    const jobId = started.job_id;
    const tick = async () => {
      try {
        const job = await fetchMediaOrganizeAuditJob(jobId);
        jobLogs.value = job.logs ?? [];
        nextTick(() => {
          if (logBox.value) {
            logBox.value.scrollTop = logBox.value.scrollHeight;
          }
        });
        if (!job.done) {
          return;
        }
        stopPolling();
        if (job.failed) {
          toast.error(job.error || "体检失败");
          scanning.value = false;
          return;
        }
        if (job.report_id) {
          await applyReport(await fetchMediaOrganizeAudit(job.report_id));
        }
        scanning.value = false;
      } catch (error) {
        stopPolling();
        scanning.value = false;
        toast.error(getApiErrorMessage(error, "获取体检进度失败"));
      }
    };
    pollTimer = window.setInterval(() => void tick(), 1000);
    await tick();
  } catch (error) {
    // 老版本后端没有异步接口时退回同步调用
    try {
      await applyReport(await runMediaOrganizeAudit(payload.value));
    } catch (fallbackError) {
      toast.error(getApiErrorMessage(fallbackError, "体检失败"));
    } finally {
      scanning.value = false;
    }
  }
}

function normalizeGroup(group: MediaAuditGroup): MediaAuditGroup {
  const completeness = group.completeness
    ? {
        ...group.completeness,
        seasons: (group.completeness.seasons ?? []).map((s) => ({
          ...s,
          missing: s.missing ?? [],
          extra: s.extra ?? [],
        })),
        missing_seasons: group.completeness.missing_seasons ?? [],
      }
    : undefined;
  const franchise = group.franchise
    ? {
        ...group.franchise,
        local: group.franchise.local ?? [],
        missing: group.franchise.missing ?? [],
        optional: group.franchise.optional ?? [],
        upcoming: group.franchise.upcoming ?? [],
      }
    : undefined;
  return {
    ...group,
    missing: group.missing ?? [],
    mixed_ext: group.mixed_ext ?? false,
    seasons: (group.seasons ?? []).map((s) => ({ ...s, missing: s.missing ?? [] })),
    completeness,
    franchise,
  };
}

/** 缺集（排除「编号差异」这种只提示的） */
function missingEpisodes(
  group: MediaAuditGroup,
): { season: number; eps: number[]; unknown: boolean; local: number; aired: number }[] {
  return (group.completeness?.seasons ?? [])
    .filter((s) => s.missing.length > 0)
    .map((s) => ({ season: s.season, eps: s.missing, unknown: s.unknown, local: s.local, aired: s.aired }));
}

function airingSeasons(group: MediaAuditGroup) {
  return (group.completeness?.seasons ?? []).filter((s) => s.airing);
}

function isBad(group: MediaAuditGroup) {
  if (!group.ok) {
    return true;
  }
  if (group.match === "unmatched" || group.match === "failed" || group.match === "unparsed") {
    // 匹配不上/解析不出季集也属于「需要你看一眼」，勾选只看不合格时一并显示
    return true;
  }
  if ((group.completeness?.missing_seasons?.length ?? 0) > 0) {
    return true;
  }
  if (missingEpisodes(group).some((m) => !m.unknown)) {
    return true;
  }
  return (group.franchise?.missing?.length ?? 0) > 0;
}

const visibleIds = computed(() => rows.value.map((g) => g.id));
const allVisibleSelected = computed(
  () => visibleIds.value.length > 0 && visibleIds.value.every((id) => selected.value[id]),
);

function toggleSelected(id: string, on: boolean) {
  selected.value = { ...selected.value, [id]: on };
}

function toggleAllVisible(on: boolean) {
  const next = { ...selected.value };
  for (const id of visibleIds.value) {
    next[id] = on;
  }
  selected.value = next;
}

function selectedGroupIds() {
  return Object.entries(selected.value)
    .filter(([, on]) => on)
    .map(([id]) => id);
}

function pad2(n: number) {
  return String(n).padStart(2, "0");
}

function episodeList(eps: number[]) {
  return eps.map((e) => `E${pad2(e)}`).join("、");
}

/** 同一合集可能落在多个作品目录里，结论只在第一条上展开，其余显示「见同系列」 */
function computeFranchiseFirst(list: MediaAuditGroup[]) {
  const seen = new Set<string>();
  const first = new Set<string>();
  for (const g of list) {
    const name = g.franchise?.name;
    if (!name || seen.has(name)) {
      continue;
    }
    seen.add(name);
    first.add(g.id);
  }
  return first;
}

const rows = computed<MediaAuditGroup[]>(() => {
  const groups = (report.value?.groups ?? []).map(normalizeGroup);
  return onlyBad.value ? groups.filter(isBad) : groups;
});

/** 同一合集只在第一条作品上展开结论，其余行显示引用，避免重复刷屏 */
const franchiseFirstIds = computed(() => computeFranchiseFirst(rows.value));

const summary = computed(() => report.value?.summary);

function seasonCount(group: MediaAuditGroup) {
  return group.seasons.length;
}

function sampleOf(group: MediaAuditGroup) {
  return group.seasons[0]?.sample ?? "";
}

function openMovePicker() {
  if (!report.value) {
    toast.error("请先体检");
    return;
  }
  if (selectedCount.value === 0) {
    toast.error("请先勾选要归集的作品");
    return;
  }
  movePickerOpen.value = true;
}

function onMoveTargetPicked(payload: { accountId: number; parentId: string; path: string }) {
  moveTargetId.value = payload.parentId;
  moveTargetName.value = payload.path || "/";
  movePickerOpen.value = false;
  moveResult.value = null;
}

async function doMove(dryRun: boolean) {
  const data = report.value;
  if (!data) {
    return;
  }
  if (!moveTargetId.value) {
    toast.error("请先选择目标目录");
    return;
  }
  const ids = selectedGroupIds();
  if (ids.length === 0) {
    toast.error("请先勾选要归集的作品");
    return;
  }
  moving.value = true;
  try {
    const result = await moveMediaOrganizeAudit(data.id, {
      target_id: moveTargetId.value,
      dry_run: dryRun,
      group_ids: ids,
    });
    moveResult.value = result;
    if (dryRun) {
      toast.success(`将移动 ${result.total} 部、${result.files} 个文件`);
    } else {
      toast.success(`已移动 ${result.moved} 部${result.failed ? `，失败 ${result.failed} 部` : ""}`);
      if (result.moved > 0) {
        await startAudit();
      }
    }
  } catch (error) {
    toast.error(getApiErrorMessage(error, dryRun ? "预览失败" : "移动失败"));
  } finally {
    moving.value = false;
  }
}

function exportCsv() {
  const data = report.value;
  if (!data) {
    return;
  }
  const lines = [
    "作品,路径,目录,样本文件,文件数,体积(字节),缺失,缺失集数,标签混合,扩展名混用,TMDB,匹配,缺失季,缺失集,多余集,缺续集",
  ];
  for (const g of data.groups) {
    const group = normalizeGroup(g);
    const missSeasons = (group.completeness?.missing_seasons ?? []).map((n) => `S${pad2(n)}`).join("+");
    const absoluteText = group.completeness?.absolute
      ? group.completeness.absolute.missing.length
        ? `编号差异:本地${group.completeness.absolute.local}集/TMDB${group.completeness.absolute.aired}集,缺第${group.completeness.absolute.range}集`
        : `季划分不同,${group.completeness.absolute.local}集齐全`
      : "";
    const missEpisodes = absoluteText
      ? absoluteText
      : missingEpisodes(group)
      .map((m) =>
        m.unknown
          ? `S${pad2(m.season)}:编号差异(本地${m.local}集/TMDB${m.aired}集)`
          : `S${pad2(m.season)}:${m.eps.map((e) => `E${pad2(e)}`).join("、")}`,
      )
      .join(" | ");
    const extraEpisodes = (group.completeness?.seasons ?? [])
      .filter((s) => s.extra.length > 0)
      .map((s) => `S${pad2(s.season)}:${s.extra.map((e) => `E${pad2(e)}`).join("、")}`)
      .join(" | ");
    const missMovies = (group.franchise?.missing ?? []).map((p) => p.title).join("、");
    for (const s of group.seasons) {
      lines.push(
        [
          g.name,
          s.dir_path || g.path,
          s.dir_path,
          s.sample,
          String(s.files),
          String(s.bytes),
          s.missing.map(labelOf).join("+"),
          (s.missing ?? [])
            .map((k) => {
              const miss = s.missing_files?.[k] ?? 0;
              const total = s.total ?? 0;
              return miss > 0 && total > 0 ? `${labelOf(k)} ${miss}/${total}` : labelOf(k);
            })
            .join("+"),
          s.mixed ? "是" : "",
          g.mixed_ext ? "是" : "",
          g.tmdb_id ? `tmdb-${g.tmdb_id}` : "",
          g.match === "matched" ? "已匹配" : g.match === "unmatched" ? "未匹配" : g.match === "failed" ? "查询失败" : "",
          missSeasons,
          missEpisodes,
          extraEpisodes,
          missMovies,
        ]
          .map((cell) => `"${String(cell).replace(/"/g, '""')}"`)
          .join(","),
      );
    }
  }
  const blob = new Blob([`\ufeff${lines.join("\n")}`], { type: "text/csv;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `media-audit-${data.id}.csv`;
  a.click();
  URL.revokeObjectURL(url);
}

onBeforeUnmount(() => stopPolling());

onMounted(async () => {
  void accountsStore.loadAccounts();
  try {
    const settings = await fetchMediaOrganizeSettings();
    const raw = settings.media_tag_order;
    const order = typeof raw === "string" ? (JSON.parse(raw) as unknown) : raw;
    if (Array.isArray(order) && order.length > 0) {
      requiredTags.value = order.filter((key): key is string => typeof key === "string" && key in missingLabels);
    }
  } catch {
    // 取不到设置就按默认三项显示，不影响体检本身
  }
});
</script>

<template>
  <div class="audit-panel">
    <div class="audit-toolbar">
      <AccountFolderField
        :display="rootName"
        placeholder="选择要体检的目录"
        @browse="openPicker"
      />
      <label class="audit-unit">
        <span>归集单位</span>
        <select v-model="unit" class="audit-select" title="剧集选「整部作品」；一片一目录的电影库也选这个；分平铺目录选「按目录」">
          <option value="show">整部作品</option>
          <option value="season">按目录</option>
        </select>
      </label>
      <label class="audit-unit" :title="`按「目录整理 → 媒体信息顺序」判定文件名里的媒体标签是否齐全，并检查同季扩展名是否混用（当前检查：${requiredLabel}）`">
        <input v-model="checkMediaInfo" type="checkbox" />
        查媒体信息
      </label>
      <label class="audit-unit" title="TMDB 已播出、本地整季都没有的季（如第 3 季在播而本地只有两季）；每部剧只多 1 次请求">
        <input v-model="checkSeasons" type="checkbox" />
        查缺季
      </label>
      <label class="audit-unit" title="按 TMDB 季集表比对：某季缺了哪几集；每季多 1 次请求，较慢">
        <input v-model="checkEpisodes" type="checkbox" />
        查缺集
      </label>
      <label class="audit-unit" title="按 TMDB 合集比对：这一系列还有哪部没收录">
        <input v-model="checkFranchise" type="checkbox" />
        查电影续集
      </label>
      <AppButton
        type="button"
        variant="primary"
        :disabled="scanning || !anyCheck"
        :title="anyCheck ? '' : '至少选择一项检查'"
        @click="startAudit"
      >
        {{ scanning ? "扫描中…" : "开始体检" }}
      </AppButton>
      <AppButton v-if="report" type="button" variant="ghost" @click="exportCsv">导出 CSV</AppButton>
    </div>

    <details class="audit-help">
      <summary>体检说明</summary>
      <ul>
        <li>
          <b>媒体信息</b>（一个开关管两项）：① 按「目录整理 → 媒体信息顺序」的检查项
          （当前：<b>{{ requiredLabel }}</b>）判定文件名里有没有这些标签，<b>缺一集就记</b>——
          整理是逐文件拼标签的，个别集缺会直接体现成同一季命名不一致，徽标上的 <b>8/24</b> 表示 24 集里有 8 集缺；
          ② 同一季混用多种媒体扩展名会标「扩展名混用」，整理时会被「扩展名不一致时跳过整部剧」挡下。
          特典（SP / OVA / NCOP / 花絮）不参与判定。
        </li>
        <li>
          <b>内容完整性</b>：把文件名里的季集号与 TMDB 比对，报「缺哪一季」「某季缺哪几集」和「系列电影缺哪部」。
          只读不改文件；在播季只算已播出集，Season 0 与特典（SP/OVA）不参与。
        </li>
        <li>
          <b>不合格</b>：以上任一项命中即计入不合格，可勾「只看不合格」筛选；
          表格第一列是<b>归集勾选</b>（默认勾上有问题的作品），选好目标目录后只搬勾中的作品。
        </li>
      </ul>
    </details>

    <div v-if="summary" class="audit-summary">
      <span>分组 <b>{{ summary.total_groups }}</b></span>
      <span :class="{ 'audit-summary__bad': summary.bad_groups > 0 }">
        不合格 <b>{{ summary.bad_groups }}</b>
      </span>
      <span>文件 <b>{{ summary.total_files }}</b></span>
      <span>不合格体积 <b>{{ formatSize(summary.bad_bytes) }}</b></span>
      <span>扫描目录 <b>{{ summary.scanned_dirs }}</b></span>
      <span v-if="(summary.missing_seasons ?? 0) > 0" class="audit-summary__bad">
        缺季 <b>{{ summary.missing_seasons }}</b>
      </span>
      <span v-if="(summary.missing_episodes ?? 0) > 0" class="audit-summary__bad">
        缺集 <b>{{ summary.missing_episodes }}</b>
      </span>
      <span v-if="(summary.missing_movies ?? 0) > 0" class="audit-summary__bad">
        缺续集 <b>{{ summary.missing_movies }}</b>
      </span>
      <span v-if="(summary.unmatched_groups ?? 0) > 0">未匹配 <b>{{ summary.unmatched_groups }}</b></span>
      <label class="audit-filter">
        <input v-model="onlyBad" type="checkbox" />
        只看不合格
      </label>
    </div>

    <div v-if="report && summary && (selectedCount > 0 || summary.bad_groups > 0)" class="audit-move">
      <span class="audit-move__label">归集选中作品（已选 {{ selectedCount }} 部）</span>
      <AccountFolderField
        :display="moveTargetName"
        placeholder="选择归集目标目录"
        @browse="openMovePicker"
      />
      <AppButton type="button" variant="ghost" :disabled="moving || selectedCount === 0" @click="doMove(true)">预览移动</AppButton>
      <AppButton
        type="button"
        variant="primary"
        :disabled="moving || selectedCount === 0 || !moveResult || moveResult.dry_run !== true"
        @click="doMove(false)"
      >
        {{ moving ? "处理中…" : "执行移动" }}
      </AppButton>
      <span v-if="moveResult && !moveResult.dry_run && moveResult.failed" class="audit-move__failed">
        失败：{{ (moveResult.items ?? []).filter((i) => !i.ok && !i.skipped).map((i) => i.name).join("、") }}
      </span>
      <span v-if="moveResult" class="audit-move__result">
        <template v-if="moveResult.dry_run">
          将移动 <b>{{ moveResult.total }}</b> 部 / <b>{{ moveResult.files }}</b> 文件 /
          <b>{{ formatSize(moveResult.bytes) }}</b>
          <template v-if="moveResult.would_create">（目标目录将自动创建）</template>
        </template>
        <template v-else>
          已移动 <b>{{ moveResult.moved }}</b> 部，失败 <b>{{ moveResult.failed }}</b> 部
        </template>
      </span>
    </div>

    <div v-if="jobLogs.length" class="audit-log-wrap">
      <button type="button" class="audit-log__title audit-log__toggle" @click="logOpen = !logOpen">
        {{ scanning ? "体检进行中，进度实时输出" : "本次体检日志" }} · {{ jobLogs.length }} 行
        <span class="audit-log__chevron">{{ scanning || logOpen ? "收起" : "展开" }}</span>
      </button>
      <pre v-show="scanning || logOpen" ref="logBox" class="audit-log">{{ jobLogs.map((l) => `[${l.time}] ${l.message}`).join("\n") }}</pre>
    </div>

    <AdminEmptyState
      v-if="!report && !scanning && !jobLogs.length"
      icon="hand-folder"
      title="还没有体检结果"
      description="选一个目录后开始体检：先查媒体信息标签是否齐全、有没有扩展名混用，再按 TMDB 查缺季、缺集与缺失的电影续集。"
    />

    <div v-else-if="scanning" class="audit-loading">体检进行中，结果会显示在下方…</div>

    <div v-else-if="!rows.length" class="audit-loading">没有需要处理的条目</div>

    <div v-else class="admin-panel-table-wrap">
      <table class="admin-table audit-table">
        <thead>
          <tr>
            <th class="audit-table__pick-col">
              <input
                type="checkbox"
                :checked="allVisibleSelected"
                title="全选当前列出的作品"
                @change="toggleAllVisible(($event.target as HTMLInputElement).checked)"
              />
            </th>
            <th>作品</th>
            <th class="audit-table__season-col">目录</th>
            <th>样本文件</th>
            <th class="audit-table__missing-col">缺失</th>
            <th class="audit-table__complete-col">季集 / 合集</th>
            <th class="audit-table__num-col">文件</th>
            <th class="audit-table__num-col">体积</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="group in rows" :key="group.id">
            <td class="audit-table__pick-col">
              <input
                type="checkbox"
                :checked="!!selected[group.id]"
                @change="toggleSelected(group.id, ($event.target as HTMLInputElement).checked)"
              />
            </td>
            <td>
              <div class="audit-name" :title="group.path">{{ group.name }}</div>
              <div v-if="group.mixed" class="audit-sub">同季签名不一致</div>
            </td>
            <td class="audit-table__season-col">{{ seasonCount(group) }}</td>
            <td class="audit-sample" :title="sampleOf(group)">{{ sampleOf(group) }}</td>
            <td class="audit-table__missing-col">
              <span
                v-for="key in group.missing ?? []"
                :key="key"
                :class="['audit-badge', isPartialMissing(group, key) ? 'audit-badge--warn' : '']"
                :title="missingHint(group, key)"
              >
                {{ labelOf(key)
                }}<template v-if="isPartialMissing(group, key)">
                  {{ missingFiles(group, key) }}/{{ group.missing_total }}</template>
              </span>
              <span
                v-if="group.mixed_ext"
                class="audit-badge audit-badge--warn"
                title="同一季里混用了多种媒体扩展名（如 mkv + mp4），整理时会被整部跳过"
              >扩展名混用</span>
              <span v-if="!(group.missing ?? []).length && !group.mixed_ext" class="audit-ok">齐全</span>
            </td>
            <td class="audit-table__complete-col">
              <template v-if="group.completeness && group.completeness.absolute">
                <span
                  v-if="!group.completeness.absolute.missing.length"
                  class="audit-ok"
                  :title="`TMDB 按播出季切分，本地按合集切分；按连续集号核对，共 ${group.completeness.absolute.local} 集齐全`"
                >季划分不同，集数齐全</span>
                <span
                  v-else
                  class="audit-badge audit-badge--warn"
                  :title="`本地 ${group.completeness.absolute.local} 集 / TMDB ${group.completeness.absolute.aired} 集；季划分不同，按连续集号比对`"
                >编号差异 缺第 {{ group.completeness.absolute.range }} 集</span>
                <span
                  v-for="n in group.completeness.missing_seasons"
                  :key="`absms-${n}`"
                  class="audit-badge"
                >缺 S{{ pad2(n) }}</span>
              </template>
              <template v-else-if="group.completeness">
                <span
                  v-for="n in group.completeness.missing_seasons"
                  :key="`ms-${n}`"
                  class="audit-badge"
                >缺 S{{ pad2(n) }}</span>
                <span
                  v-for="m in missingEpisodes(group)"
                  :key="`me-${m.season}`"
                  :class="m.unknown ? 'audit-badge audit-badge--warn' : 'audit-badge'"
                  :title="m.unknown ? `本地 ${m.local} 集 / TMDB ${m.aired} 集，集号对不上，需人工确认` : ''"
                >
                  <template v-if="m.unknown">S{{ pad2(m.season) }} 编号差异</template>
                  <template v-else>S{{ pad2(m.season) }} 缺 {{ episodeList(m.eps) }}</template>
                </span>
                <span
                  v-for="s in airingSeasons(group)"
                  :key="`air-${s.season}`"
                  class="audit-ok"
                >S{{ pad2(s.season) }} 在播 {{ s.aired }}/{{ s.total }}</span>
                <span
                  v-if="!group.completeness.missing_seasons.length && !missingEpisodes(group).length"
                  class="audit-ok"
                >季集齐全</span>
                <span v-if="group.completeness.unreliable" class="audit-badge audit-badge--warn">编号差异</span>
              </template>
              <template v-else-if="group.franchise && franchiseFirstIds.has(group.id)">
                <span v-if="!group.franchise.missing.length" class="audit-ok" :title="`TMDB 合集共 ${group.franchise.parts} 部，本地收录 ${group.franchise.local.length} 部`">
                  已收录齐全<template v-if="group.franchise.optional.length || group.franchise.upcoming.length">
                    （{{ group.franchise.local.length }}/{{ group.franchise.parts }}）</template>
                </span>
                <span v-else class="audit-badge">缺 {{ group.franchise.missing.map((p) => p.title).join('、') }}</span>
                <span v-for="p in group.franchise.optional" :key="`opt-${p.id}`" class="audit-badge audit-badge--warn" :title="p.reason ?? ''">可选 {{ p.title }}</span>
                <span v-if="group.franchise.upcoming.length" class="audit-ok">待上映 {{ group.franchise.upcoming.length }}</span>
              </template>
              <span v-else-if="group.franchise" class="audit-ok" :title="'同一合集的结论合并显示在该系列的第一部作品上'">
                同《{{ group.franchise.name }}》合集
              </span>
              <span
                v-else-if="group.match === 'unparsed'"
                class="audit-badge audit-badge--warn"
                title="文件名里既没有 SxxExx，所在目录也不是季目录，无法判断本地有哪几季"
              >未识别季集</span>
              <span v-else-if="group.match === 'unmatched'" class="audit-badge audit-badge--warn">未匹配</span>
              <span v-else-if="group.match === 'failed'" class="audit-badge audit-badge--warn">查询失败</span>
              <span v-else class="audit-ok">—</span>
            </td>
            <td class="audit-table__num-col">{{ group.files }}</td>
            <td class="audit-table__num-col">{{ formatSize(group.bytes) }}</td>
          </tr>
        </tbody>
      </table>
    </div>

    <FolderPickerModal
      :open="movePickerOpen"
      :account-id="accountId || null"
      :accounts="activeAccounts"
      :initial-path="moveTargetName"
      title="选择归集目标目录"
      @close="movePickerOpen = false"
      @resolve="onMoveTargetPicked"
    />

    <FolderPickerModal
      :open="pickerOpen"
      selectable-account
      :accounts="activeAccounts"
      :account-id="accountId || null"
      :initial-path="rootName"
      title="选择要体检的目录"
      @close="pickerOpen = false"
      @resolve="onFolderPicked"
    />
  </div>
</template>

<style scoped>
.audit-panel {
  display: flex;
  flex-direction: column;
  gap: 16px;
}

.audit-toolbar {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}

.audit-unit {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 13px;
  color: var(--text-muted);
}

.audit-select {
  padding: 6px 8px;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  background: var(--surface);
  color: var(--text);
}

.audit-help {
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  font-size: 13px;
  color: var(--text-muted);
}

.audit-help summary {
  cursor: pointer;
  color: var(--text);
}

.audit-help ul {
  margin: 8px 0 0;
  padding-left: 18px;
  display: flex;
  flex-direction: column;
  gap: 6px;
  line-height: 1.6;
}

.audit-help b {
  color: var(--text);
}

.audit-summary {
  display: flex;
  align-items: center;
  gap: 16px;
  flex-wrap: wrap;
  font-size: 13px;
  color: var(--text-muted);
}

.audit-summary b {
  color: var(--text);
}

.audit-summary__bad b {
  color: var(--danger, #e5484d);
}

.audit-filter {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-left: auto;
}

.audit-move {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
  padding: 12px;
  border: 1px dashed var(--border);
  border-radius: var(--radius-sm);
}

.audit-move__label {
  font-size: 13px;
  color: var(--text-muted);
}

.audit-move__failed {
  font-size: 13px;
  color: var(--danger, #e5484d);
}

.audit-move__result {
  font-size: 13px;
  color: var(--text-muted);
}

.audit-log__toggle {
  display: flex;
  width: 100%;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin: 0 0 8px;
  padding: 0;
  border: none;
  background: none;
  color: inherit;
  font: inherit;
  cursor: pointer;
  text-align: left;
}

.audit-log__chevron {
  color: var(--text-muted);
  font-size: 12px;
}

.audit-log-wrap {
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  overflow: hidden;
}

.audit-log__title {
  padding: 8px 12px;
  font-size: 13px;
  color: var(--text-muted);
  border-bottom: 1px solid var(--border);
}

.audit-log {
  margin: 0;
  padding: 10px 12px;
  max-height: 320px;
  overflow: auto;
  font-size: 12px;
  line-height: 1.6;
  font-family: var(--font-mono, ui-monospace, SFMono-Regular, Menlo, monospace);
  color: var(--text-muted);
  background: var(--surface-2, transparent);
  white-space: pre-wrap;
  word-break: break-all;
}

.audit-loading {
  padding: 32px 0;
  text-align: center;
  color: var(--text-muted);
  font-size: 13px;
}

.audit-table__season-col,
.audit-table__num-col {
  width: 72px;
  text-align: right;
  white-space: nowrap;
}

.audit-table__missing-col {
  width: 200px;
}

.audit-table__pick-col {
  width: 36px;
  text-align: center;
}

.audit-table__complete-col {
  min-width: 220px;
}

.audit-table__complete-col .audit-ok {
  display: inline-block;
  margin-right: 8px;
}

.audit-name {
  font-weight: 500;
}

.audit-sub {
  font-size: 12px;
  color: var(--text-muted);
}

.audit-sample {
  max-width: 420px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 13px;
  color: var(--text-muted);
}

.audit-badge {
  display: inline-block;
  margin-right: 6px;
  padding: 2px 8px;
  border-radius: 999px;
  background: color-mix(in srgb, var(--danger, #e5484d) 14%, transparent);
  color: var(--danger, #e5484d);
  font-size: 12px;
  white-space: nowrap;
}

.audit-badge--warn {
  background: color-mix(in srgb, #f59e0b 16%, transparent);
  color: #f59e0b;
}

.audit-ok {
  font-size: 12px;
  color: var(--text-muted);
}
</style>
