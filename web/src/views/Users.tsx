import { useCallback, useEffect, useState } from 'react'
import { api } from '../api'
import type { Role, User } from '../types'

export default function Users({ onChange, me }: { onChange?: () => void; me: User | null }) {
  const [users, setUsers] = useState<User[]>([])
  const [name, setName] = useState('')
  const [role, setRole] = useState<Role>('user')
  const [err, setErr] = useState('')

  const refresh = useCallback(async () => { setUsers((await api.listUsers()) || []) }, [])
  useEffect(() => { refresh() }, [refresh])

  const add = async () => {
    setErr('')
    if (!name) return
    try {
      await api.putUser({ name, role })
      setName('')
      await refresh(); onChange?.()
    } catch (e) { setErr((e as Error).message) }
  }
  const del = async (id: string) => {
    try { await api.deleteUser(id); await refresh(); onChange?.() }
    catch (e) { setErr((e as Error).message) }
  }

  return (
    <div className="main" style={{ maxWidth: 760, margin: '0 auto' }}>
      <div className="card">
        <h2>Users</h2>
        <div className="muted" style={{ marginBottom: 10 }}>
          RBAC: admin &gt; user &gt; script. Identity is dev-mode (no passwords yet) — pick the acting
          user from the top-right selector to view as them. Admins manage users and any script; users
          see and manage only their own.
        </div>
        <div className="row" style={{ marginBottom: 12 }}>
          <input placeholder="name" value={name} onChange={(e) => setName(e.target.value)} />
          <select value={role} onChange={(e) => setRole(e.target.value as Role)}>
            <option value="user">user</option>
            <option value="admin">admin</option>
          </select>
          <button onClick={add}>Add user</button>
        </div>
        {err && <div className="err" style={{ marginBottom: 8 }}>{err}</div>}
        <table>
          <thead><tr><th>name</th><th>role</th><th>id</th><th /></tr></thead>
          <tbody>
            {users.map((u) => (
              <tr key={u.id}>
                <td>{u.name}</td>
                <td><span className={'badge ' + (u.role === 'admin' ? 'failed' : 'suspended')}>{u.role}</span></td>
                <td className="mono muted">{u.id}</td>
                <td>{u.id === me?.id ? <span className="muted">you</span> : <button onClick={() => del(u.id)}>delete</button>}</td>
              </tr>
            ))}
            {users.length === 0 && <tr><td colSpan={4} className="muted">no users yet</td></tr>}
          </tbody>
        </table>
      </div>
    </div>
  )
}
