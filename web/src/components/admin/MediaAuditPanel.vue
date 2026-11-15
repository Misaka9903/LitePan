<script setup lang="ts">
import { computed, onMounted, ref } from "vue";
import { storeToRefs } from "pinia";
import { getApiErrorMessage } from "@/api/client";
import type { MediaAuditGroup, MediaAuditReport } from "@/api/mediaOrganize";
import { runMediaOrganizeAudit } from "@/api/mediaOrganize";
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
const rootId = ref("");
const rootName = ref("");
const accountId = ref<number>(0);
const scanning = ref(false);
const report = ref<MediaAuditReport | null>(null);
const onlyBad = ref(true);
const unit = ref<"show" | "season">("show");

const missingLabels: Record<string, string> = {
  screen_size: "分辨率",
  video_codec: "视频编码",
  audio_codec: "音频编码",
  audio_channels: "声道",
  frame_rate: "帧率",
};

function labelOf(key: string) {
  return missingLabels[key] ?? key;
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

async function startAudit() {
  if (!rootId.value) {
    toast.error("请先选择要体检的目录");
    return;
  }
  scanning.value = true;
  try {
    const data = await runMediaOrganizeAudit({
      account_id: accountId.value,
      root_id: rootId.value,
      root_name: rootName.value,
      unit: unit.value,
    });
    report.value = data;
    if (data.summary.bad_groups === 0) {
      toast.success(`体检完成：${data.summary.total_groups} 部全部齐全`);
    } else {
      toast.success(`体检完成：${data.summary.bad_groups} 部信息不全`);
    }
  } catch (error) {
    toast.error(getApiErrorMessage(error, "体检失败"));
  } finally {
    scanning.value = false;
  }
}

const rows = computed<MediaAuditGroup[]>(() => {
  const groups = report.value?.groups ?? [];
  return onlyBad.value ? groups.filter((g) => !g.ok) : groups;
});

const summary = computed(() => report.value?.summary);

function seasonCount(group: MediaAuditGroup) {
  return group.seasons.length;
}

function sampleOf(group: MediaAuditGroup) {
  return group.seasons[0]?.sample ?? "";
}

function exportCsv() {
  const data = report.value;
  if (!data) {
    return;
  }
  const lines = ["剧集,路径,季目录,样本文件,文件数,体积(字节),缺失,混合"];
  for (const g of data.groups) {
    for (const s of g.seasons) {
      lines.push(
        [
          g.name,
          s.dir_path || g.path,
          s.dir_path,
          s.sample,
          String(s.files),
          String(s.bytes),
          s.missing.map(labelOf).join("+"),
          s.mixed ? "是" : "",
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

onMounted(() => {
  void accountsStore.loadAccounts();
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
        <select v-model="unit" class="audit-select">
          <option value="show">整部剧</option>
          <option value="season">按季</option>
        </select>
      </label>
      <AppButton type="button" variant="primary" :disabled="scanning" @click="startAudit">
        {{ scanning ? "扫描中…" : "开始体检" }}
      </AppButton>
      <AppButton v-if="report" type="button" variant="ghost" @click="exportCsv">导出 CSV</AppButton>
    </div>

    <div v-if="summary" class="audit-summary">
      <span>分组 <b>{{ summary.total_groups }}</b></span>
      <span :class="{ 'audit-summary__bad': summary.bad_groups > 0 }">
        不合格 <b>{{ summary.bad_groups }}</b>
      </span>
      <span>文件 <b>{{ summary.total_files }}</b></span>
      <span>不合格体积 <b>{{ formatSize(summary.bad_bytes) }}</b></span>
      <span>扫描目录 <b>{{ summary.scanned_dirs }}</b></span>
      <span class="audit-summary__hint">{{ summary.used_full_list ? "全量列表一次拉取" : "逐目录递归" }}</span>
      <label class="audit-filter">
        <input v-model="onlyBad" type="checkbox" />
        只看不合格
      </label>
    </div>

    <AdminEmptyState
      v-if="!report && !scanning"
      icon="hand-folder"
      title="还没有体检结果"
      description="选一个目录后开始体检：按文件名里解析出的分辨率、视频编码、音频编码判定，缺项的剧会被列出来。"
    />

    <div v-else-if="scanning" class="audit-loading">正在拉取目录树并判定…</div>

    <div v-else-if="!rows.length" class="audit-loading">没有需要处理的条目</div>

    <div v-else class="admin-panel-table-wrap">
      <table class="admin-table audit-table">
        <thead>
          <tr>
            <th>剧集</th>
            <th class="audit-table__season-col">季</th>
            <th>样本文件</th>
            <th class="audit-table__missing-col">缺失</th>
            <th class="audit-table__num-col">文件</th>
            <th class="audit-table__num-col">体积</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="group in rows" :key="group.id">
            <td>
              <div class="audit-name" :title="group.path">{{ group.name }}</div>
              <div v-if="group.mixed" class="audit-sub">同季签名不一致</div>
            </td>
            <td class="audit-table__season-col">{{ seasonCount(group) }}</td>
            <td class="audit-sample" :title="sampleOf(group)">{{ sampleOf(group) }}</td>
            <td class="audit-table__missing-col">
              <span v-for="key in group.missing" :key="key" class="audit-badge">{{ labelOf(key) }}</span>
              <span v-if="!group.missing.length" class="audit-ok">齐全</span>
            </td>
            <td class="audit-table__num-col">{{ group.files }}</td>
            <td class="audit-table__num-col">{{ formatSize(group.bytes) }}</td>
          </tr>
        </tbody>
      </table>
    </div>

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

.audit-summary__hint {
  opacity: 0.7;
}

.audit-filter {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-left: auto;
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

.audit-ok {
  font-size: 12px;
  color: var(--text-muted);
}
</style>
