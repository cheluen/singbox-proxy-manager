// 拖拽排序封装（@dnd-kit）：支持鼠标、触屏与键盘（聚焦手柄后 空格 抓起，方向键移动，空格 放下）。
import { DndContext, KeyboardSensor, PointerSensor, closestCenter, useSensor, useSensors } from '@dnd-kit/core'
import { SortableContext, sortableKeyboardCoordinates, useSortable, verticalListSortingStrategy } from '@dnd-kit/sortable'
import { CSS } from '@dnd-kit/utilities'
import { GripVertical } from 'lucide-react'

export function SortableList({ ids, disabled, onReorder, children }) {
  const sensors = useSensors(
    useSensor(PointerSensor, { activationConstraint: { distance: 6 } }),
    useSensor(KeyboardSensor, { coordinateGetter: sortableKeyboardCoordinates })
  )
  return (
    <DndContext
      sensors={sensors}
      collisionDetection={closestCenter}
      onDragEnd={({ active, over }) => {
        if (over && active.id !== over.id) onReorder(active.id, over.id)
      }}
    >
      <SortableContext items={ids} disabled={disabled} strategy={verticalListSortingStrategy}>
        {children}
      </SortableContext>
    </DndContext>
  )
}

// 因此拖拽过程中本组件因上下文变化而重绘时，行内容不会跟着重绘。
export function SortableItem({ id, disabled, handleLabel, className = '', children, ...rest }) {
  const { attributes, listeners, setNodeRef, setActivatorNodeRef, transform, transition, isDragging } = useSortable({ id, disabled })
  return (
    <div
      ref={setNodeRef}
      className={`${className}${isDragging ? ' is-dragging' : ''}`}
      style={{ transform: CSS.Translate.toString(transform), transition }}
      {...rest}
    >
      <button
        type="button"
        ref={setActivatorNodeRef}
        className="sb-drag"
        data-tip={handleLabel}
        data-testid={`node-drag-handle-${id}`}
        {...attributes}
        {...listeners}
        disabled={disabled}
        aria-label={handleLabel}
        aria-disabled={disabled}
        onClick={(event) => event.stopPropagation()}
      >
        <GripVertical size={15} strokeWidth={1.75} aria-hidden="true" />
      </button>
      {children}
    </div>
  )
}
