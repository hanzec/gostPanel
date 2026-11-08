import { ElMessageBox } from 'element-plus'

export function confirmPanelAction(message, title, options = {}) {
  return ElMessageBox.confirm(message, title, {
    modalClass: 'panel-dialog-overlay',
    ...options,
    customClass: options.customClass ? `panel-confirm ${options.customClass}` : 'panel-confirm'
  })
}
