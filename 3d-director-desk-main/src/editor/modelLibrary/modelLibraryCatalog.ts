import guoCharactersManifest from "./guoCharactersManifest.json";
import guoPropsManifest from "./guoPropsManifest.json";
import type { CharacterImportReadiness, CharacterRigProfile } from "../schema/directorProject";
import type { DirectorCharacterBoneMap } from "../schema/semanticBody";
import { getGuoCharacterCompatibility } from "./guoCharacterCompatibility";

export const LOCAL_GUO_ASSETS_AVAILABLE = __LOCAL_GUO_ASSETS_AVAILABLE__;
export const LOCAL_MIXAMO_CHARACTER_AVAILABLE = __LOCAL_MIXAMO_CHARACTER_AVAILABLE__;

export type ModelLibraryCategoryId = "characters" | "convenience" | "home" | "outdoor" | "tools" | "weapons" | "my-models";

export type ModelLibraryCategory = {
  directoryName: string;
  id: ModelLibraryCategoryId;
  label: string;
};

export type ModelLibraryItem = {
  categoryId: ModelLibraryCategoryId;
  fileName: string;
  id: string;
  name: string;
  thumbUrl?: string;
  url: string;
  kind?: "character" | "prop";
  characterRigProfile?: CharacterRigProfile;
  characterImportReadiness?: CharacterImportReadiness;
  characterOrientationCorrection?: [number, number, number];
  characterBoneMap?: DirectorCharacterBoneMap;
};

export function getModelLibraryCharacterStatus(item: ModelLibraryItem) {
  if (item.kind !== "character") return null;
  if (item.characterImportReadiness === "ready") return "可用动作";
  if (item.characterImportReadiness === "native-only") return "仅自带动作";
  if (item.characterImportReadiness === "manual-mapping") return "需骨架映射";
  if (item.characterImportReadiness === "static-only") return "仅静态";
  return "未体检";
}

export const MODEL_LIBRARY_CATEGORIES: ModelLibraryCategory[] = [
  { id: "characters", label: "人物", directoryName: "人物" },
  { id: "convenience", label: "便利生活", directoryName: "便利生活" },
  { id: "home", label: "居家生活", directoryName: "生活家居" },
  { id: "outdoor", label: "户外出行", directoryName: "户外出行" },
  { id: "tools", label: "工具配件", directoryName: "工具配件" },
  { id: "weapons", label: "武器", directoryName: "武器" },
  { id: "my-models", label: "我的模型", directoryName: "" },
];

function createBuiltInThumbnail(name: string, categoryId: ModelLibraryCategoryId) {
  const colors: Record<ModelLibraryCategoryId, [string, string]> = {
    characters: ["#38506b", "#8cc7eb"],
    convenience: ["#295b78", "#59b7da"],
    home: ["#6d4d3d", "#d49a6a"],
    outdoor: ["#315c49", "#72bd83"],
    tools: ["#62522f", "#d1aa50"],
    weapons: ["#5c3b40", "#d08a92"],
    "my-models": ["#4d5561", "#98a2b3"],
  };
  const [background, accent] = colors[categoryId];
  const label = name.slice(0, 2);
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" width="192" height="128" viewBox="0 0 192 128"><rect width="192" height="128" rx="8" fill="${background}"/><path d="M24 91h144M38 76h116l-14-32H58z" fill="none" stroke="${accent}" stroke-width="5" stroke-linecap="round" stroke-linejoin="round" opacity=".72"/><circle cx="66" cy="91" r="10" fill="${background}" stroke="${accent}" stroke-width="5"/><circle cx="130" cy="91" r="10" fill="${background}" stroke="${accent}" stroke-width="5"/><text x="96" y="31" text-anchor="middle" fill="#fff" font-family="system-ui,sans-serif" font-size="17" font-weight="700">${label}</text></svg>`;
  return `data:image/svg+xml,${encodeURIComponent(svg)}`;
}

const BUILTIN_LIFE_MODEL_INPUTS: Array<Omit<ModelLibraryItem, "id" | "thumbUrl" | "url">> = [
  { categoryId: "convenience", fileName: "ATM_low.fbx", name: "自动取款机" },
  { categoryId: "convenience", fileName: "trash_sorting_low.fbx", name: "分类垃圾桶" },
  { categoryId: "home", fileName: "sofa_modern_low.fbx", name: "沙发" },
  { categoryId: "home", fileName: "dining_table_low.fbx", name: "餐桌" },
  { categoryId: "home", fileName: "refrigerator_modern_low.fbx", name: "冰箱" },
  { categoryId: "home", fileName: "washing_machine_modern_low.fbx", name: "洗衣机" },
  { categoryId: "outdoor", fileName: "sedan_low.fbx", name: "家用轿车" },
  { categoryId: "outdoor", fileName: "suv_city_low.fbx", name: "城市SUV" },
  { categoryId: "outdoor", fileName: "city_bus_low.fbx", name: "城市公交车" },
  { categoryId: "outdoor", fileName: "bicycle_city_low.fbx", name: "自行车" },
  { categoryId: "outdoor", fileName: "electric_scooter_low.fbx", name: "电动踏板车" },
  { categoryId: "outdoor", fileName: "street_lamp_low.fbx", name: "路灯" },
  { categoryId: "outdoor", fileName: "street_tree_low.fbx", name: "绿化树" },
  { categoryId: "outdoor", fileName: "backpack_low.fbx", name: "背包" },
  { categoryId: "outdoor", fileName: "thermus_low.fbx", name: "保温瓶" },
  { categoryId: "outdoor", fileName: "deer_skull_low.fbx", name: "鹿头骨" },
  { categoryId: "tools", fileName: "wrench_low.fbx", name: "扳手" },
  { categoryId: "tools", fileName: "drill_press_low.fbx", name: "台钻" },
];

export const BUILTIN_LIFE_MODELS: ModelLibraryItem[] = BUILTIN_LIFE_MODEL_INPUTS.map((item) => ({
  ...item,
  id: `builtin:${item.fileName}`,
  url: `builtin://life/${item.fileName}`,
  thumbUrl: createBuiltInThumbnail(item.name, item.categoryId),
}));

const storyboardAssetUrl = (path: string) =>
  `${import.meta.env.BASE_URL}${path.split("/").map(encodeURIComponent).join("/")}`;

type StoryboardModelInput = [
  categoryId: ModelLibraryCategoryId,
  name: string,
  path: string,
  kind?: "character" | "prop",
  readiness?: CharacterImportReadiness,
  rig?: CharacterRigProfile,
];

const STORYBOARD_MODEL_INPUTS: StoryboardModelInput[] = [
  ["characters", "成年女性", "characters/essential/adult-female/adult-female-lod.glb", "character", "ready", "mixamo"],
  ["characters", "成年男性", "characters/essential/adult-male/adult-male-lod.glb", "character", "ready", "mixamo"],
  ["characters", "婴儿", "characters/essential/baby/baby-lod.glb", "character", "ready", "mixamo"],
  ["characters", "儿童", "characters/essential/child/child-lod.glb", "character", "ready", "mixamo"],
  ["characters", "少女", "characters/essential/teen-female/teen-female-lod.glb", "character", "ready", "mixamo"],
  ["characters", "少年", "characters/essential/teen-male/teen-male-lod.glb", "character", "ready", "mixamo"],
  ["characters", "Charles", "characters/extra/charles/charles.glb", "character", "ready", "mixamo"],
  ["characters", "猫", "characters/extra/cat/cat.glb", "character", "static-only"],
  ["characters", "狗", "characters/extra/dog/dog.glb", "character", "static-only"],
  ["characters", "马 · 静态", "characters/extra/horse/horse.glb", "character", "static-only"],
  ["characters", "蛇", "characters/extra/snake/snake.glb", "character", "static-only"],
  ["home", "双人床", "objects/essential/bed/bed-full/bed-full.glb"],
  ["home", "特大床", "objects/essential/bed/bed-king/bed-king.glb"],
  ["home", "单人床", "objects/essential/bed/bed-twin/bed-twin.glb"],
  ["home", "书架柜", "objects/essential/cabinet/cabinet-bookshelf/cabinet-bookshelf.glb"],
  ["home", "三屉柜", "objects/essential/cabinet/cabinet-dresser-3/cabinet-dresser-3.glb"],
  ["home", "五屉柜", "objects/essential/cabinet/cabinet-dresser-5/cabinet-dresser-5.glb"],
  ["home", "吧椅", "objects/essential/chair/chair-bar/chair-bar.glb"],
  ["home", "方椅", "objects/essential/chair/chair-box/chair-box.glb"],
  ["home", "现代椅", "objects/essential/chair/chair-modern/chair-modern.glb"],
  ["home", "办公椅", "objects/essential/chair/chair-office/chair-office.glb"],
  ["home", "沙发椅", "objects/essential/chair/chair-sofa/chair-sofa.glb"],
  ["home", "宽沙发", "objects/essential/chair/chair-sofa-wide/chair-sofa-wide.glb"],
  ["home", "高凳", "objects/essential/chair/chair-stool/chair-stool.glb"],
  ["home", "矮凳", "objects/essential/chair/chair-stool-mini/chair-stool-mini.glb"],
  ["home", "双开门", "objects/essential/door/door-double/door-double.glb"],
  ["home", "双开门框", "objects/essential/door/door-double-frame/door-double-frame.glb"],
  ["home", "单开门", "objects/essential/door/door-single/door-single.glb"],
  ["home", "单开门框", "objects/essential/door/door-single-frame/door-single-frame.glb"],
  ["home", "吧台", "objects/essential/table/table-bar/table-bar.glb"],
  ["home", "圆吧台", "objects/essential/table/table-bar-circle/table-bar-circle.glb"],
  ["home", "长吧台", "objects/essential/table/table-bar-rectangle/table-bar-rectangle.glb"],
  ["home", "咖啡桌", "objects/essential/table/table-coffee/table-coffee.glb"],
  ["home", "柜台", "objects/essential/table/table-counter/table-counter.glb"],
  ["home", "床头柜", "objects/essential/table/table-nightstand/table-nightstand.glb"],
  ["home", "圆餐桌", "objects/essential/table/table-sit-circle/table-sit-circle.glb"],
  ["home", "长餐桌", "objects/essential/table/table-sit-rectangle/table-sit-rectangle.glb"],
  ["home", "方餐桌", "objects/essential/table/table-sit-square/table-sit-square.glb"],
  ["tools", "圆柱体", "objects/essential/primitive/primitive-cylinder/primitive-cylinder.glb"],
  ["tools", "空心圆柱", "objects/essential/primitive/primitive-cylinder-hollow/primitive-cylinder-hollow.glb"],
  ["tools", "金字塔", "objects/essential/primitive/primitive-pyramid/primitive-pyramid.glb"],
  ["tools", "球体", "objects/essential/primitive/primitive-sphere/primitive-sphere.glb"],
  ["tools", "楔体", "objects/essential/primitive/primitive-wedge/primitive-wedge.glb"],
  ["tools", "楼梯", "objects/essential/stairs/stairs-single-6/stairs-single-6.glb"],
  ["outdoor", "茂盛树木", "objects/essential/tree/tree-bushy/tree-bushy.glb"],
  ["outdoor", "枯树", "objects/essential/tree/tree-no-leaves/tree-no-leaves.glb"],
  ["outdoor", "BMX 自行车", "objects/essential/vehicle/vehicle-bicycle-bmx/vehicle-bicycle-bmx.glb"],
  ["outdoor", "轿车", "objects/essential/vehicle/vehicle-car/vehicle-car.glb"],
  ["convenience", "背包", "objects/extra/Back Pack/Back_Pack.glb"],
  ["home", "床头灯", "objects/extra/bedside lamp/Bedside_tableLamp.glb"],
  ["home", "床边桌", "objects/extra/bedside_table/bedside_table.glb"],
  ["convenience", "黑色提包 01", "objects/extra/black bags/Blackbag1.glb"],
  ["convenience", "黑色提包 02", "objects/extra/black bags/Blackbag2.glb"],
  ["home", "咖啡桌 A", "objects/extra/coffee table/coffee_table.glb"],
  ["home", "咖啡桌 B", "objects/extra/coffee table/coffee_table_variation.glb"],
  ["outdoor", "消防栓", "objects/extra/fire hydrant/FireHydrant.glb"],
  ["home", "单人沙发", "objects/extra/furniture 1man/furniture_1man.glb"],
  ["home", "L 形沙发", "objects/extra/furniture L shape/furniture_long.glb"],
  ["outdoor", "金属垃圾桶", "objects/extra/metal bin/Metal_Bin.glb"],
  ["home", "办公椅 · 写实", "objects/extra/Office_chair_common/Office_chair.glb"],
  ["home", "办公桌 · 写实", "objects/extra/Office_Table_common/office_table.glb"],
  ["convenience", "手机", "objects/extra/phone/phone.glb"],
  ["outdoor", "警车 · 无窗", "objects/extra/police car/police(nowindows.glb"],
  ["outdoor", "警车", "objects/extra/police car/police.glb"],
  ["convenience", "游戏手柄", "objects/extra/ps4-dualshock/ps4-dualshock.glb"],
  ["home", "课桌", "objects/extra/School_desk/desk.glb"],
  ["outdoor", "滑板", "objects/extra/Skateboard/Skateboard.glb"],
  ["outdoor", "地铁列车", "objects/extra/subway-train/subway-train.glb"],
  ["home", "方桌 · 写实", "objects/extra/table/Table.glb"],
  ["outdoor", "交通锥", "objects/extra/traffic cone/Road_Hazzard_Cone.glb"],
  ["outdoor", "树木 · 写实", "objects/extra/tree with leaves/Tree1.glb"],
  ["outdoor", "枯树 · 写实", "objects/extra/tree without leaves/Tree2_noleaves.glb"],
  ["outdoor", "滑板公园", "environments/skatepark/skatepark.glb"],
  ["outdoor", "金字塔遗迹", "environments/ziggurat/ziggurat.glb"],
];

const GENERIC_HUMANOID_BONE_MAP: DirectorCharacterBoneMap = {
  head: "Head",
  chest: "Spine2",
  waist: "Hips",
  leftUpperArm: "LeftArm",
  leftForearm: "LeftForeArm",
  leftHand: "LeftHand",
  rightUpperArm: "RightArm",
  rightForearm: "RightForeArm",
  rightHand: "RightHand",
  leftThigh: "LeftUpLeg",
  leftCalf: "LeftLeg",
  leftFoot: "LeftFoot",
  rightThigh: "RightUpLeg",
  rightCalf: "RightLeg",
  rightFoot: "RightFoot",
};

export const STORYBOARD_MODELS: ModelLibraryItem[] = STORYBOARD_MODEL_INPUTS.map(
  ([categoryId, name, path, kind = "prop", characterImportReadiness, characterRigProfile]) => ({
    id: `storyboard:${path}`,
    categoryId,
    fileName: path.split("/").pop() ?? "model.glb",
    name,
    url: storyboardAssetUrl(path),
    thumbUrl: createBuiltInThumbnail(name, categoryId),
    kind,
    characterImportReadiness,
    characterRigProfile,
    characterBoneMap: path.startsWith("characters/essential/") ? GENERIC_HUMANOID_BONE_MAP : undefined,
  })
);

type GuoCharacterManifestItem = {
  id: string;
  label: string;
  localModelPath: string;
  localThumbnailPath: string;
};

type GuoPropManifestItem = {
  id: string;
  label: string;
  categoryId: string;
  localModelPath: string;
  localThumbnailPath: string;
};

const localAssetUrl = (path: string) => `${import.meta.env.BASE_URL}local-assets/guo-3d-assets/${path}`;
const localMixamoAssetUrl = (path: string) => `${import.meta.env.BASE_URL}local-assets/mixamo/${path}`;

export const MIXAMO_CHARACTER_MODELS: ModelLibraryItem[] = LOCAL_MIXAMO_CHARACTER_AVAILABLE
  ? [{
      id: "mixamo-character:camille",
      kind: "character",
      categoryId: "characters",
      fileName: "camille.fbx",
      name: "Camille（Mixamo）",
      url: localMixamoAssetUrl("characters/camille.fbx"),
      characterRigProfile: "mixamo",
      characterImportReadiness: "ready",
      characterOrientationCorrection: [0, 0, 0],
    }, {
      id: "rigged-character:robot-expressive",
      kind: "character",
      categoryId: "characters",
      fileName: "robot-expressive.glb",
      name: "表情机器人（自带动作）",
      url: localMixamoAssetUrl("characters/robot-expressive.glb"),
      characterRigProfile: "mixamo",
      characterImportReadiness: "ready",
      characterOrientationCorrection: [0, 0, 0],
    }, {
      id: "mixamo-character:xbot",
      kind: "character",
      categoryId: "characters",
      fileName: "xbot.glb",
      name: "XBot（Mixamo）",
      url: localMixamoAssetUrl("characters/xbot.glb"),
      characterRigProfile: "mixamo",
      characterImportReadiness: "ready",
      characterOrientationCorrection: [0, 0, 0],
    }, {
      id: "mixamo-character:soldier",
      kind: "character",
      categoryId: "characters",
      fileName: "soldier.glb",
      name: "Soldier（Mixamo）",
      url: localMixamoAssetUrl("characters/soldier.glb"),
      characterRigProfile: "mixamo",
      characterImportReadiness: "ready",
      characterOrientationCorrection: [0, 0, 0],
    }]
  : [];

export const GUO_CHARACTER_MODELS: ModelLibraryItem[] = (guoCharactersManifest.items as GuoCharacterManifestItem[]).map((item) => {
  const compatibility = getGuoCharacterCompatibility(item.id);
  return {
    id: `guo-character:${item.id}`,
    kind: "character",
    categoryId: "characters",
    fileName: item.localModelPath.split("/").pop() ?? `${item.id}.fbx`,
    name: item.label,
    url: localAssetUrl(`guo-skeleton-models/${item.localModelPath}`),
    thumbUrl: localAssetUrl(`guo-skeleton-models/${item.localThumbnailPath}`),
    characterRigProfile: compatibility.rigProfile,
    characterImportReadiness: compatibility.readiness,
    characterOrientationCorrection: compatibility.orientationCorrection,
  };
});

function mapGuoPropCategory(categoryId: string): ModelLibraryCategoryId {
  if (categoryId === "furniture") return "home";
  if (categoryId === "vehicle" || categoryId === "environment") return "outdoor";
  if (categoryId === "firearms" || categoryId === "melee") return "weapons";
  if (categoryId === "accessory") return "convenience";
  return "tools";
}

export const GUO_PROP_MODELS: ModelLibraryItem[] = (guoPropsManifest.items as GuoPropManifestItem[]).map((item) => ({
  id: `guo-prop:${item.id}`,
  kind: "prop",
  categoryId: mapGuoPropCategory(item.categoryId),
  fileName: item.localModelPath.split("/").pop() ?? `${item.id}.fbx`,
  name: item.label,
  url: localAssetUrl(`guo-mounted-props-200/${item.localModelPath}`),
  thumbUrl: localAssetUrl(`guo-mounted-props-200/${item.localThumbnailPath}`),
}));

export function getModelLibraryItems() {
  const localModels = LOCAL_GUO_ASSETS_AVAILABLE ? [...GUO_CHARACTER_MODELS, ...GUO_PROP_MODELS] : [];
  return [...STORYBOARD_MODELS, ...MIXAMO_CHARACTER_MODELS, ...localModels, ...BUILTIN_LIFE_MODELS].sort((a, b) => {
    const categoryIndexA = MODEL_LIBRARY_CATEGORIES.findIndex((category) => category.id === a.categoryId);
    const categoryIndexB = MODEL_LIBRARY_CATEGORIES.findIndex((category) => category.id === b.categoryId);

    if (categoryIndexA !== categoryIndexB) return categoryIndexA - categoryIndexB;

    return a.name.localeCompare(b.name, "zh-CN");
  });
}
