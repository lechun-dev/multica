"use client";

import { useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Loader2, Pencil, Plus, Trash2 } from "lucide-react";
import { toast } from "sonner";
import { useWorkspaceId } from "@multica/core/hooks";
import {
  useCreateWorkspaceRuntimeModel,
  useDeleteWorkspaceRuntimeModel,
  useUpdateWorkspaceRuntimeModel,
  workspaceRuntimeModelListOptions,
} from "@multica/core/workspace-runtime-models";
import type {
  RuntimeModelThinkingLevel,
  WorkspaceRuntimeModel,
} from "@multica/core/types";
import { Badge } from "@multica/ui/components/ui/badge";
import { Button } from "@multica/ui/components/ui/button";
import { Checkbox } from "@multica/ui/components/ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Input } from "@multica/ui/components/ui/input";
import { Label } from "@multica/ui/components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@multica/ui/components/ui/select";
import { Switch } from "@multica/ui/components/ui/switch";
import { Textarea } from "@multica/ui/components/ui/textarea";
import { useT } from "../../i18n";
import { SettingsCard, SettingsSection, SettingsTab } from "./settings-layout";

const THINKING_LEVELS: RuntimeModelThinkingLevel[] = [
  { value: "none", label: "None" },
  { value: "minimal", label: "Minimal" },
  { value: "low", label: "Low" },
  { value: "medium", label: "Medium" },
  { value: "high", label: "High" },
  { value: "xhigh", label: "Extra high" },
  { value: "max", label: "Max" },
  { value: "ultra", label: "Ultra" },
];

const RUNTIME_DEFAULT = "__runtime_default__";

type RuntimeModelDraft = {
  modelId: string;
  displayName: string;
  modelProvider: string;
  description: string;
  thinkingLevelValues: string[];
  defaultThinkingLevel: string;
  enabled: boolean;
  sortOrder: string;
};

const EMPTY_DRAFT: RuntimeModelDraft = {
  modelId: "",
  displayName: "",
  modelProvider: "openai",
  description: "",
  thinkingLevelValues: [],
  defaultThinkingLevel: "",
  enabled: true,
  sortOrder: "0",
};

function draftForModel(model: WorkspaceRuntimeModel): RuntimeModelDraft {
  return {
    modelId: model.model_id,
    displayName: model.display_name,
    modelProvider: model.model_provider,
    description: model.description,
    thinkingLevelValues: model.thinking_levels.map((level) => level.value),
    defaultThinkingLevel: model.default_thinking_level,
    enabled: model.enabled,
    sortOrder: String(model.sort_order),
  };
}

export function RuntimeModelsTab() {
  const { t } = useT("settings");
  const workspaceId = useWorkspaceId();
  const { data: models = [], isLoading, isError } = useQuery(
    workspaceRuntimeModelListOptions(workspaceId),
  );
  const createModel = useCreateWorkspaceRuntimeModel();
  const updateModel = useUpdateWorkspaceRuntimeModel();
  const deleteModel = useDeleteWorkspaceRuntimeModel();
  const [editing, setEditing] = useState<WorkspaceRuntimeModel | null>(null);
  const [creating, setCreating] = useState(false);
  const [draft, setDraft] = useState<RuntimeModelDraft>(EMPTY_DRAFT);

  const dialogOpen = creating || editing !== null;
  const isSaving = createModel.isPending || updateModel.isPending;
  const selectedThinkingLevels = useMemo(
    () =>
      THINKING_LEVELS.filter((level) =>
        draft.thinkingLevelValues.includes(level.value),
      ),
    [draft.thinkingLevelValues],
  );
  const defaultThinkingItems = [
    {
      value: RUNTIME_DEFAULT,
      label: t(($) => $.runtime_models.runtime_default),
    },
    ...selectedThinkingLevels.map((level) => ({
      value: level.value,
      label: level.label,
    })),
  ];

  const openCreate = () => {
    setDraft(EMPTY_DRAFT);
    setEditing(null);
    setCreating(true);
  };

  const openEdit = (model: WorkspaceRuntimeModel) => {
    setDraft(draftForModel(model));
    setEditing(model);
    setCreating(false);
  };

  const closeDialog = () => {
    if (!isSaving) {
      setCreating(false);
      setEditing(null);
    }
  };

  const toggleThinkingLevel = (value: string, checked: boolean) => {
    const thinkingLevelValues = checked
      ? [...draft.thinkingLevelValues, value]
      : draft.thinkingLevelValues.filter((item) => item !== value);
    setDraft({
      ...draft,
      thinkingLevelValues,
      defaultThinkingLevel:
        draft.defaultThinkingLevel === value && !checked
          ? ""
          : draft.defaultThinkingLevel,
    });
  };

  const save = async () => {
    const sortOrder = Number(draft.sortOrder);
    if (
      !draft.modelId.trim() ||
      !draft.displayName.trim() ||
      !draft.modelProvider.trim() ||
      !Number.isInteger(sortOrder) ||
      sortOrder < 0
    ) {
      toast.error(t(($) => $.runtime_models.invalid));
      return;
    }

    const payload = {
      display_name: draft.displayName.trim(),
      model_provider: draft.modelProvider.trim(),
      description: draft.description.trim(),
      thinking_levels: selectedThinkingLevels,
      default_thinking_level: draft.defaultThinkingLevel,
      enabled: draft.enabled,
      sort_order: sortOrder,
    };

    try {
      if (editing) {
        await updateModel.mutateAsync({ modelId: editing.id, ...payload });
      } else {
        await createModel.mutateAsync({
          runtime_provider: "codex",
          model_id: draft.modelId.trim(),
          ...payload,
        });
      }
      toast.success(t(($) => $.runtime_models.saved));
      closeDialog();
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.runtime_models.save_failed),
      );
    }
  };

  const toggleEnabled = async (
    model: WorkspaceRuntimeModel,
    enabled: boolean,
  ) => {
    try {
      await updateModel.mutateAsync({ modelId: model.id, enabled });
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.runtime_models.save_failed),
      );
    }
  };

  const remove = async (model: WorkspaceRuntimeModel) => {
    if (
      !window.confirm(
        t(($) => $.runtime_models.delete_confirm, {
          name: model.display_name,
        }),
      )
    ) {
      return;
    }
    try {
      await deleteModel.mutateAsync(model.id);
      toast.success(t(($) => $.runtime_models.deleted));
    } catch (error) {
      toast.error(
        error instanceof Error
          ? error.message
          : t(($) => $.runtime_models.delete_failed),
      );
    }
  };

  return (
    <SettingsTab
      title={t(($) => $.runtime_models.title)}
      description={t(($) => $.runtime_models.description)}
    >
      <SettingsSection
        action={
          <Button size="sm" onClick={openCreate}>
            <Plus aria-hidden="true" className="mr-1 size-4" />
            {t(($) => $.runtime_models.add)}
          </Button>
        }
      >
        <SettingsCard>
          {isLoading ? (
            <div className="p-8 text-center text-muted-foreground">
              {t(($) => $.runtime_models.loading)}
            </div>
          ) : isError ? (
            <div className="p-8 text-center text-destructive">
              {t(($) => $.runtime_models.load_failed)}
            </div>
          ) : models.length === 0 ? (
            <div className="p-8 text-center text-muted-foreground">
              {t(($) => $.runtime_models.empty)}
            </div>
          ) : (
            models.map((model) => (
              <div
                key={model.id}
                className="flex flex-col gap-3 px-4 py-4 sm:flex-row sm:items-center sm:gap-4"
              >
                <div className="min-w-0 flex-1">
                  <div className="flex min-w-0 flex-wrap items-center gap-2">
                    <span className="truncate font-medium">
                      {model.display_name}
                    </span>
                    <Badge variant={model.enabled ? "secondary" : "outline"}>
                      {model.enabled
                        ? t(($) => $.runtime_models.enabled)
                        : t(($) => $.runtime_models.disabled)}
                    </Badge>
                  </div>
                  <div className="mt-1 break-all text-caption text-muted-foreground">
                    {model.model_id} · {model.model_provider}
                  </div>
                  {model.thinking_levels.length > 0 ? (
                    <div className="mt-1 text-caption text-muted-foreground">
                      {t(($) => $.runtime_models.thinking_summary, {
                        levels: model.thinking_levels
                          .map((level) => level.label)
                          .join(", "),
                      })}
                    </div>
                  ) : null}
                </div>
                <div className="flex items-center gap-2">
                  <Switch
                    checked={model.enabled}
                    disabled={updateModel.isPending}
                    onCheckedChange={(enabled) =>
                      void toggleEnabled(model, enabled)
                    }
                    aria-label={t(($) => $.runtime_models.toggle_enabled, {
                      name: model.display_name,
                    })}
                  />
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    onClick={() => openEdit(model)}
                    aria-label={t(($) => $.runtime_models.edit)}
                  >
                    <Pencil className="size-4" />
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    disabled={deleteModel.isPending}
                    onClick={() => void remove(model)}
                    aria-label={t(($) => $.runtime_models.delete)}
                  >
                    <Trash2 className="size-4" />
                  </Button>
                </div>
              </div>
            ))
          )}
        </SettingsCard>
      </SettingsSection>

      <Dialog open={dialogOpen} onOpenChange={(open) => !open && closeDialog()}>
        <DialogContent className="sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>
              {creating
                ? t(($) => $.runtime_models.create_title)
                : t(($) => $.runtime_models.edit_title)}
            </DialogTitle>
            <DialogDescription>
              {t(($) => $.runtime_models.form_description)}
            </DialogDescription>
          </DialogHeader>
          <div className="grid max-h-[65vh] gap-4 overflow-y-auto px-0.5 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="runtime-model-id">
                {t(($) => $.runtime_models.model_id)}
              </Label>
              <Input
                id="runtime-model-id"
                value={draft.modelId}
                disabled={editing !== null}
                placeholder={t(($) => $.runtime_models.model_id_placeholder)}
                onChange={(event) =>
                  setDraft({ ...draft, modelId: event.target.value })
                }
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="runtime-model-name">
                {t(($) => $.runtime_models.display_name)}
              </Label>
              <Input
                id="runtime-model-name"
                value={draft.displayName}
                placeholder={t(($) => $.runtime_models.display_name_placeholder)}
                onChange={(event) =>
                  setDraft({ ...draft, displayName: event.target.value })
                }
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="runtime-model-provider">
                {t(($) => $.runtime_models.model_provider)}
              </Label>
              <Input
                id="runtime-model-provider"
                value={draft.modelProvider}
                onChange={(event) =>
                  setDraft({ ...draft, modelProvider: event.target.value })
                }
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="runtime-model-order">
                {t(($) => $.runtime_models.sort_order)}
              </Label>
              <Input
                id="runtime-model-order"
                type="number"
                min="0"
                step="1"
                value={draft.sortOrder}
                onChange={(event) =>
                  setDraft({ ...draft, sortOrder: event.target.value })
                }
              />
            </div>
            <div className="space-y-1.5 sm:col-span-2">
              <Label htmlFor="runtime-model-description">
                {t(($) => $.runtime_models.description_label)}
              </Label>
              <Textarea
                id="runtime-model-description"
                value={draft.description}
                placeholder={t(($) => $.runtime_models.description_placeholder)}
                onChange={(event) =>
                  setDraft({ ...draft, description: event.target.value })
                }
              />
            </div>
            <fieldset className="space-y-2 sm:col-span-2">
              <legend className="text-body font-medium">
                {t(($) => $.runtime_models.thinking_levels)}
              </legend>
              <p className="text-caption text-muted-foreground">
                {t(($) => $.runtime_models.thinking_levels_hint)}
              </p>
              <div className="grid gap-2 pt-1 sm:grid-cols-4">
                {THINKING_LEVELS.map((level) => (
                  <label
                    key={level.value}
                    className="flex min-h-9 cursor-pointer items-center gap-2 rounded-md border border-input px-3 py-2 text-body"
                  >
                    <Checkbox
                      checked={draft.thinkingLevelValues.includes(level.value)}
                      onCheckedChange={(checked) =>
                        toggleThinkingLevel(level.value, checked)
                      }
                    />
                    {level.label}
                  </label>
                ))}
              </div>
            </fieldset>
            <div className="space-y-1.5">
              <Label>{t(($) => $.runtime_models.default_thinking_level)}</Label>
              <Select
                items={defaultThinkingItems}
                value={draft.defaultThinkingLevel || RUNTIME_DEFAULT}
                onValueChange={(value) =>
                  setDraft({
                    ...draft,
                    defaultThinkingLevel:
                      value === RUNTIME_DEFAULT ? "" : (value ?? ""),
                  })
                }
              >
                <SelectTrigger className="w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {defaultThinkingItems.map((item) => (
                    <SelectItem key={item.value} value={item.value}>
                      {item.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <label className="flex items-end gap-2 pb-2 text-body">
              <Switch
                checked={draft.enabled}
                onCheckedChange={(enabled) =>
                  setDraft({ ...draft, enabled })
                }
              />
              {t(($) => $.runtime_models.enabled_on_save)}
            </label>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={closeDialog}>
              {t(($) => $.runtime_models.cancel)}
            </Button>
            <Button onClick={() => void save()} disabled={isSaving}>
              {isSaving ? (
                <Loader2 aria-hidden="true" className="mr-1 size-4 animate-spin" />
              ) : null}
              {t(($) => $.runtime_models.save)}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </SettingsTab>
  );
}
